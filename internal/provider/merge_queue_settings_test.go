package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeMergeQueueSettingsAPI mimics the merge queue settings API of the
// Mergify API for one organization.
type fakeMergeQueueSettingsAPI struct {
	mu    sync.Mutex
	owner string
	repos []string
	// Whether the organization may set `batch_pull_request` to `false`.
	falseAvailable bool
	organization   *bool
	repositories   map[string]*bool
	branches       map[string]map[string]*bool // repository -> branch name or pattern -> value
	// Every request, as `<method> <escaped path>`.
	requests []string
}

func newFakeMergeQueueSettingsAPI(t *testing.T) (*fakeMergeQueueSettingsAPI, *httptest.Server) {
	api := &fakeMergeQueueSettingsAPI{
		owner:          "acme",
		repos:          []string{"engine", "docs"},
		falseAvailable: true,
		repositories:   map[string]*bool{},
		branches:       map[string]map[string]*bool{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, srv
}

func boolPointer(v bool) *bool {
	return &v
}

func (a *fakeMergeQueueSettingsAPI) set(update func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	update()
}

func (a *fakeMergeQueueSettingsAPI) removeRepository(name string) {
	a.set(func() {
		a.repos = slices.DeleteFunc(a.repos, func(r string) bool { return r == name })
	})
}

// isValidPattern stands for the API's glob pattern check, which refuses an
// unbalanced brace.
func isValidPattern(branch string) bool {
	depth := 0
	for i := 0; i < len(branch); i++ {
		switch branch[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func (a *fakeMergeQueueSettingsAPI) effective(repository, branch string) (bool, map[string]any) {
	if v := a.branches[repository][branch]; v != nil {
		return *v, map[string]any{"level": "branch", "branch": branch}
	}
	patterns := []string{}
	for pattern := range a.branches[repository] {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	for _, pattern := range patterns {
		if matched, _ := pathpkg.Match(pattern, branch); matched && a.branches[repository][pattern] != nil {
			return *a.branches[repository][pattern], map[string]any{"level": "branch", "branch": pattern}
		}
	}
	if v := a.repositories[repository]; v != nil {
		return *v, map[string]any{"level": "repository", "branch": nil}
	}
	if a.organization != nil {
		return *a.organization, map[string]any{"level": "organization", "branch": nil}
	}
	return true, map[string]any{"level": "default", "branch": nil}
}

func (a *fakeMergeQueueSettingsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.EscapedPath())

	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Unauthorized"})
		return
	}
	// Split on the escaped path, so that a branch sent with an unescaped
	// slash is not found, as a client relying on it would be ambiguous.
	parts := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	for i, part := range parts {
		parts[i], _ = url.PathUnescape(part)
	}
	notFound := func() { writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"}) }

	var value **bool
	var branchLevel map[string]*bool
	var branch string
	switch {
	case len(parts) == 4 && parts[0] == "orgs" && parts[2] == "merge-queue" && parts[3] == "settings":
		if parts[1] != a.owner {
			notFound()
			return
		}
		value = &a.organization
	case len(parts) >= 5 && parts[0] == "repos":
		if parts[1] != a.owner || !slices.Contains(a.repos, parts[2]) {
			notFound()
			return
		}
		repository := parts[2]
		rest := strings.Join(parts[3:], "\x00")
		switch {
		case rest == "merge-queue\x00settings":
			v := a.repositories[repository]
			value = &v
			defer func() { a.repositories[repository] = v }()
		case len(parts) == 7 && parts[3] == "merge-queue" && parts[4] == "settings" && parts[5] == "branches":
			if a.branches[repository] == nil {
				a.branches[repository] = map[string]*bool{}
			}
			branchLevel, branch = a.branches[repository], parts[6]
			v := branchLevel[branch]
			value = &v
			defer func() {
				if v == nil {
					delete(branchLevel, branch)
				} else {
					branchLevel[branch] = v
				}
			}()
		case len(parts) == 7 && parts[3] == "branches" && parts[5] == "merge-queue" && parts[6] == "settings" && r.Method == http.MethodGet:
			effective, source := a.effective(repository, parts[4])
			writeJSON(w, http.StatusOK, map[string]any{
				"branch":   parts[4],
				"settings": map[string]any{"batch_pull_request": effective},
				"sources":  map[string]any{"batch_pull_request": source},
			})
			return
		default:
			notFound()
			return
		}
	default:
		notFound()
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"batch_pull_request": *value})
	case http.MethodPut:
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
			return
		}
		var settings MergeQueueSettings
		for field, raw := range body {
			if field != "batch_pull_request" {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "Extra inputs are not permitted: " + field})
				return
			}
			if err := json.Unmarshal(raw, &settings.BatchPullRequest); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
				return
			}
		}
		if branchLevel != nil && !isValidPattern(branch) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"detail":     "`" + branch + "`: unbalanced brace",
				"error_code": "MERGE_QUEUE_SETTING_INVALID_BRANCH_PATTERN",
			})
			return
		}
		if settings.BatchPullRequest != nil && !*settings.BatchPullRequest && !a.falseAvailable {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"detail":     "`batch_pull_request: false` is not available to this organization yet. Contact Mergify Support to have it enabled for your organization.",
				"error_code": "MERGE_QUEUE_SETTING_NOT_AVAILABLE",
			})
			return
		}
		*value = settings.BatchPullRequest
		writeJSON(w, http.StatusOK, settings)
	case http.MethodDelete:
		*value = nil
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
	}
}

func describe(v *bool) string {
	if v == nil {
		return "unset"
	}
	return fmt.Sprint(*v)
}

// checkFakeValue checks what a level of the fake API sets.
func checkFakeValue(api *fakeMergeQueueSettingsAPI, read func() *bool, want *bool) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if got := read(); describe(got) != describe(want) {
			return fmt.Errorf("batch_pull_request: got %s, want %s", describe(got), describe(want))
		}
		return nil
	}
}

// checkDeletedLast checks that destroying the resource called the DELETE
// route of the level, rather than writing it.
func checkDeletedLast(api *fakeMergeQueueSettingsAPI, path string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for i := len(api.requests) - 1; i >= 0; i-- {
			method, requestPath, _ := strings.Cut(api.requests[i], " ")
			if method == http.MethodGet {
				continue
			}
			if method != http.MethodDelete || requestPath != path {
				return fmt.Errorf("last write is %s, want DELETE %s", api.requests[i], path)
			}
			return nil
		}
		return fmt.Errorf("no DELETE %s", path)
	}
}

func checkRequested(api *fakeMergeQueueSettingsAPI, request string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if !slices.Contains(api.requests, request) {
			return fmt.Errorf("no %s among %v", request, api.requests)
		}
		return nil
	}
}

func organizationSettingsConfig(srv *httptest.Server, attributes string) string {
	return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_organization_merge_queue_settings" "test" {
  owner = "acme"
  %s
}
`, attributes)
}

func TestOrganizationMergeQueueSettingsResource(t *testing.T) {
	api, srv := newFakeMergeQueueSettingsAPI(t)
	organization := func() *bool { return api.organization }
	name := "mergify_organization_merge_queue_settings.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(
			checkFakeValue(api, organization, nil),
			checkDeletedLast(api, "/orgs/acme/merge-queue/settings"),
		),
		Steps: []resource.TestStep{
			{
				Config: organizationSettingsConfig(srv, "batch_pull_request = false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "acme"),
					resource.TestCheckResourceAttr(name, "batch_pull_request", "false"),
					checkFakeValue(api, organization, boolPointer(false)),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateId:     "acme",
				ImportStateVerify: true,
			},
			{
				Config: organizationSettingsConfig(srv, "batch_pull_request = true"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "batch_pull_request", "true"),
					checkFakeValue(api, organization, boolPointer(true)),
				),
			},
			{
				// Leaving the attribute out unsets it, so that the value
				// comes from the defaults.
				Config: organizationSettingsConfig(srv, ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr(name, "batch_pull_request"),
					checkFakeValue(api, organization, nil),
				),
			},
			{
				// A value set outside Terraform shows in the plan.
				PreConfig:          func() { api.set(func() { api.organization = boolPointer(false) }) },
				Config:             organizationSettingsConfig(srv, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: organizationSettingsConfig(srv, ""),
				Check:  checkFakeValue(api, organization, nil),
			},
			{
				Config: organizationSettingsConfig(srv, "batch_pull_request = false"),
				Check:  checkFakeValue(api, organization, boolPointer(false)),
			},
		},
	})
}

func TestOrganizationMergeQueueSettingsNotAvailable(t *testing.T) {
	api, srv := newFakeMergeQueueSettingsAPI(t)
	api.falseAvailable = false
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      organizationSettingsConfig(srv, "batch_pull_request = false"),
				ExpectError: regexp.MustCompile(`(?s)batch_pull_request = false is not available to this organization.*Contact\s+Mergify\s+Support`),
			},
		},
	})
}

func repositorySettingsConfig(srv *httptest.Server, repository, attributes string) string {
	return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_repository_merge_queue_settings" "test" {
  owner      = "acme"
  repository = %q
  %s
}
`, repository, attributes)
}

func TestRepositoryMergeQueueSettingsResource(t *testing.T) {
	api, srv := newFakeMergeQueueSettingsAPI(t)
	repository := func(name string) func() *bool { return func() *bool { return api.repositories[name] } }
	name := "mergify_repository_merge_queue_settings.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(
			checkFakeValue(api, repository("engine"), nil),
			checkFakeValue(api, repository("docs"), nil),
			checkDeletedLast(api, "/repos/acme/docs/merge-queue/settings"),
		),
		Steps: []resource.TestStep{
			{
				Config: repositorySettingsConfig(srv, "engine", "batch_pull_request = false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "acme/engine"),
					resource.TestCheckResourceAttr(name, "batch_pull_request", "false"),
					checkFakeValue(api, repository("engine"), boolPointer(false)),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateId:     "acme/engine",
				ImportStateVerify: true,
			},
			{
				// Another repository is another resource: the first one's
				// value is unset, not left behind.
				Config: repositorySettingsConfig(srv, "docs", "batch_pull_request = false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "acme/docs"),
					checkFakeValue(api, repository("engine"), nil),
					checkFakeValue(api, repository("docs"), boolPointer(false)),
					checkRequested(api, "DELETE /repos/acme/engine/merge-queue/settings"),
				),
			},
		},
	})
}

func TestRepositoryMergeQueueSettingsResourceOfAGoneRepository(t *testing.T) {
	api, srv := newFakeMergeQueueSettingsAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: repositorySettingsConfig(srv, "engine", "batch_pull_request = false"),
			},
			{
				// Mergify no longer knows the repository: the resource is
				// planned for creation again.
				PreConfig:          func() { api.removeRepository("engine") },
				Config:             repositorySettingsConfig(srv, "engine", "batch_pull_request = false"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func branchSettingsConfig(srv *httptest.Server, branch, attributes string) string {
	return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_branch_merge_queue_settings" "test" {
  owner      = "acme"
  repository = "engine"
  branch     = %q
  %s
}
`, branch, attributes)
}

func TestBranchMergeQueueSettingsResource(t *testing.T) {
	api, srv := newFakeMergeQueueSettingsAPI(t)
	branch := func(name string) func() *bool { return func() *bool { return api.branches["engine"][name] } }
	name := "mergify_branch_merge_queue_settings.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(
			checkFakeValue(api, branch("release/*"), nil),
			checkFakeValue(api, branch("main"), nil),
			checkDeletedLast(api, "/repos/acme/engine/merge-queue/settings/branches/main"),
		),
		Steps: []resource.TestStep{
			{
				Config: branchSettingsConfig(srv, "release/*", "batch_pull_request = false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "acme/engine:release/*"),
					resource.TestCheckResourceAttr(name, "batch_pull_request", "false"),
					checkFakeValue(api, branch("release/*"), boolPointer(false)),
					// The pattern is one path segment.
					checkRequested(api, "PUT /repos/acme/engine/merge-queue/settings/branches/release%2F%2A"),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateId:     "acme/engine:release/*",
				ImportStateVerify: true,
			},
			{
				Config: branchSettingsConfig(srv, "release/*", ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr(name, "batch_pull_request"),
					checkFakeValue(api, branch("release/*"), nil),
				),
			},
			{
				Config: branchSettingsConfig(srv, "release/*", "batch_pull_request = true"),
				Check:  checkFakeValue(api, branch("release/*"), boolPointer(true)),
			},
			{
				Config: branchSettingsConfig(srv, "main", "batch_pull_request = false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", "acme/engine:main"),
					checkFakeValue(api, branch("release/*"), nil),
					checkFakeValue(api, branch("main"), boolPointer(false)),
					checkRequested(api, "DELETE /repos/acme/engine/merge-queue/settings/branches/release%2F%2A"),
				),
			},
		},
	})
}

func TestBranchMergeQueueSettingsInvalidPattern(t *testing.T) {
	_, srv := newFakeMergeQueueSettingsAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      branchSettingsConfig(srv, "release/{1,2", "batch_pull_request = false"),
				ExpectError: regexp.MustCompile(`(?s)Invalid base branch pattern.*unbalanced brace`),
			},
		},
	})
}

func TestMergeQueueSettingsImportID(t *testing.T) {
	for _, tc := range []struct {
		scope mergeQueueSettingsScope
		id    string
		want  *MergeQueueSettingsLevel
	}{
		{organizationScope, "acme", &MergeQueueSettingsLevel{Owner: "acme"}},
		{organizationScope, "", nil},
		{organizationScope, "acme/engine", nil},
		{repositoryScope, "acme/engine", &MergeQueueSettingsLevel{Owner: "acme", Repository: "engine"}},
		{repositoryScope, "acme", nil},
		{repositoryScope, "acme/", nil},
		{repositoryScope, "acme/engine/main", nil},
		{branchScope, "acme/engine:release/*", &MergeQueueSettingsLevel{Owner: "acme", Repository: "engine", Branch: "release/*"}},
		{branchScope, "acme/engine:main", &MergeQueueSettingsLevel{Owner: "acme", Repository: "engine", Branch: "main"}},
		{branchScope, "acme/engine", nil},
		{branchScope, "acme/engine:", nil},
		{branchScope, "acme:main", nil},
	} {
		got, ok := tc.scope.parseID(tc.id)
		switch {
		case tc.want == nil && ok:
			t.Errorf("%s %q: got %+v, want refused", tc.scope.typeSuffix(), tc.id, got)
		case tc.want != nil && (!ok || got != *tc.want):
			t.Errorf("%s %q: got %+v (%v), want %+v", tc.scope.typeSuffix(), tc.id, got, ok, *tc.want)
		case ok && tc.scope.id(got) != tc.id:
			t.Errorf("%s %q: id is %q", tc.scope.typeSuffix(), tc.id, tc.scope.id(got))
		}
	}
}

const effectiveSettingsConfig = `
resource "mergify_repository_merge_queue_settings" "engine" {
  owner              = "acme"
  repository         = "engine"
  batch_pull_request = false
}

resource "mergify_branch_merge_queue_settings" "release" {
  owner              = "acme"
  repository         = "engine"
  branch             = "release/*"
  batch_pull_request = true
}

data "mergify_effective_merge_queue_settings" "main" {
  owner      = "acme"
  repository = "engine"
  branch     = "main"
  depends_on = [mergify_repository_merge_queue_settings.engine, mergify_branch_merge_queue_settings.release]
}

data "mergify_effective_merge_queue_settings" "release" {
  owner      = "acme"
  repository = "engine"
  branch     = "release/1.0"
  depends_on = [mergify_repository_merge_queue_settings.engine, mergify_branch_merge_queue_settings.release]
}

data "mergify_effective_merge_queue_settings" "docs" {
  owner      = "acme"
  repository = "docs"
  branch     = "main"
}
`

func TestEffectiveMergeQueueSettingsDataSource(t *testing.T) {
	_, srv := newFakeMergeQueueSettingsAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv) + effectiveSettingsConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.main", "id", "acme/engine:main"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.main", "batch_pull_request", "false"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.main", "batch_pull_request_source.level", "repository"),
					resource.TestCheckNoResourceAttr("data.mergify_effective_merge_queue_settings.main", "batch_pull_request_source.branch"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.release", "batch_pull_request", "true"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.release", "batch_pull_request_source.level", "branch"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.release", "batch_pull_request_source.branch", "release/*"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.docs", "batch_pull_request", "true"),
					resource.TestCheckResourceAttr("data.mergify_effective_merge_queue_settings.docs", "batch_pull_request_source.level", "default"),
				),
			},
		},
	})
}
