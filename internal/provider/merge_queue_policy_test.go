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
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakePolicy is a merge queue policy of the fake API: repository is empty
// for an organization's.
type fakePolicy struct {
	repository string
	body       MergeQueuePolicy
}

// fakeMergeQueuePoliciesAPI mimics the merge queue policy routes of the
// Mergify API for one organization, whose repositories all have `main` as
// their default branch.
type fakeMergeQueuePoliciesAPI struct {
	mu    sync.Mutex
	owner string
	repos []string
	// Whether the organization may set `batch_pull_request` to `false`.
	falseAvailable bool
	// How many policies an organization, or a repository, may hold.
	maxPolicies int
	policies    map[string]*fakePolicy
	created     int
	// Every request, as `<method> <escaped path>`.
	requests []string
}

func newFakeMergeQueuePoliciesAPI(t *testing.T) (*fakeMergeQueuePoliciesAPI, *httptest.Server) {
	api := &fakeMergeQueuePoliciesAPI{
		owner:          "acme",
		repos:          []string{"engine", "docs"},
		falseAvailable: true,
		maxPolicies:    100,
		policies:       map[string]*fakePolicy{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, srv
}

func boolPointer(v bool) *bool {
	return &v
}

func (a *fakeMergeQueuePoliciesAPI) set(update func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	update()
}

// add stores a policy as if created outside Terraform, and returns its id.
func (a *fakeMergeQueuePoliciesAPI) add(repository string, policy MergeQueuePolicy) string {
	a.created++
	policy.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", a.created)
	if policy.Enforcement == "" {
		policy.Enforcement = "active"
	}
	if repository != "" {
		policy.Repository = &repository
	}
	a.policies[policy.ID] = &fakePolicy{repository: repository, body: policy}
	return policy.ID
}

// only returns the one policy the fake holds.
func (a *fakeMergeQueuePoliciesAPI) only() (*fakePolicy, error) {
	if len(a.policies) != 1 {
		return nil, fmt.Errorf("%d policies, want 1", len(a.policies))
	}
	for _, p := range a.policies {
		return p, nil
	}
	return nil, nil
}

func writeAPIError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail, "error_code": code})
}

// checkPattern stands for the API's pattern check: `~` starts only `~ALL`
// and, for branches, `~DEFAULT_BRANCH`, and a brace must be balanced.
func checkPattern(pattern string, defaultBranchAllowed bool) string {
	if pattern == "~DEFAULT_BRANCH" && !defaultBranchAllowed {
		return "`~DEFAULT_BRANCH` names a branch, not a repository"
	}
	if strings.HasPrefix(pattern, "~") && pattern != "~ALL" && pattern != "~DEFAULT_BRANCH" {
		return "`" + pattern + "` is not a name, a glob pattern, `~ALL` or `~DEFAULT_BRANCH`"
	}
	if strings.Count(pattern, "{") != strings.Count(pattern, "}") {
		return "`" + pattern + "`: unbalanced brace"
	}
	return ""
}

func matchesPattern(pattern, name string) bool {
	switch pattern {
	case "~ALL":
		return true
	case "~DEFAULT_BRANCH":
		return name == "main"
	}
	matched, _ := pathpkg.Match(pattern, name)
	return matched
}

func (p MergeQueuePolicyPatterns) matches(name string) bool {
	return slices.ContainsFunc(p.IncludePatterns, func(pattern string) bool { return matchesPattern(pattern, name) }) &&
		!slices.ContainsFunc(p.ExcludePatterns, func(pattern string) bool { return matchesPattern(pattern, name) })
}

// resolve answers the merge queue settings of a base branch: the safest
// value, `true`, wins when the policies applying to it disagree.
func (a *fakeMergeQueuePoliciesAPI) resolve(repository, branch string) map[string]any {
	ids := make([]string, 0, len(a.policies))
	for id := range a.policies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	policies := []MergeQueuePolicy{}
	setting := map[bool][]string{}
	for _, id := range ids {
		p := a.policies[id]
		target := p.body.Target
		applies := p.body.Enforcement == "active" &&
			(p.repository == repository || (p.repository == "" && target.Repositories.matches(repository))) &&
			target.Branches.matches(branch)
		if !applies {
			continue
		}
		policies = append(policies, p.body)
		if v := p.body.Settings.BatchPullRequest; v != nil {
			setting[*v] = append(setting[*v], id)
		}
	}
	value, rule, policyIDs := true, "default", []string{}
	switch {
	case len(setting) == 2:
		rule, policyIDs = "policies_disagree", setting[true]
	case len(setting) == 1:
		rule = "policies_agree"
		for value, policyIDs = range setting {
		}
	}
	return map[string]any{
		"branch":    branch,
		"settings":  map[string]any{"batch_pull_request": value},
		"decisions": map[string]any{"batch_pull_request": map[string]any{"rule": rule, "policy_ids": policyIDs}},
		"policies":  policies,
	}
}

// decodeStrict decodes a body, refusing a field out of fields as the API's
// models do.
func decodeStrict(r *http.Request, fields ...string) (map[string]json.RawMessage, string) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err.Error()
	}
	for field := range body {
		if !slices.Contains(fields, field) {
			return nil, "Extra inputs are not permitted: " + field
		}
	}
	return body, ""
}

// validate checks a policy as the API does before writing it, and answers
// the refusal when there is one.
func (a *fakeMergeQueuePoliciesAPI) validate(w http.ResponseWriter, repository, id string, policy MergeQueuePolicy) bool {
	if (repository == "") != (policy.Target.Repositories != nil) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "target: wrong shape for the scope"})
		return false
	}
	if policy.Enforcement != "active" && policy.Enforcement != "disabled" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "enforcement: invalid"})
		return false
	}
	all := []MergeQueuePolicyPatterns{policy.Target.Branches}
	if policy.Target.Repositories != nil {
		all = append(all, *policy.Target.Repositories)
	}
	for i, patterns := range all {
		if len(patterns.IncludePatterns) == 0 || patterns.ExcludePatterns == nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "include_patterns: at least one"})
			return false
		}
		for _, pattern := range append(slices.Clone(patterns.IncludePatterns), patterns.ExcludePatterns...) {
			if problem := checkPattern(pattern, i == 0); problem != "" {
				writeAPIError(w, http.StatusUnprocessableEntity, "MERGE_QUEUE_POLICY_INVALID_PATTERN", problem+". Fix the pattern the error names.")
				return false
			}
		}
	}
	if policy.Settings.BatchPullRequest == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "settings: a policy sets at least one setting"})
		return false
	}
	for otherID, other := range a.policies {
		if otherID != id && other.repository == repository && other.body.Name == policy.Name {
			writeAPIError(w, http.StatusConflict, "MERGE_QUEUE_POLICY_NAME_TAKEN", fmt.Sprintf("Another merge queue policy is already named %q here.", policy.Name))
			return false
		}
	}
	if !*policy.Settings.BatchPullRequest && !a.falseAvailable {
		writeAPIError(w, http.StatusForbidden, "MERGE_QUEUE_SETTING_NOT_AVAILABLE",
			"`batch_pull_request: false` is not available to this organization yet. Contact Mergify Support to have it enabled for your organization.")
		return false
	}
	return true
}

func (a *fakeMergeQueuePoliciesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.EscapedPath())

	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Unauthorized"})
		return
	}
	// Split on the escaped path, so that a branch sent with an unescaped
	// slash is not found.
	parts := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	for i, part := range parts {
		parts[i], _ = url.PathUnescape(part)
	}
	notFound := func() { writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"}) }
	if len(parts) < 4 || parts[0] != "orgs" || parts[1] != a.owner {
		notFound()
		return
	}

	repository := ""
	rest := parts[2:]
	if rest[0] == "repositories" {
		if len(rest) < 4 || !slices.Contains(a.repos, rest[1]) {
			notFound()
			return
		}
		repository, rest = rest[1], rest[2:]
	}
	if len(rest) == 4 && repository != "" && rest[0] == "merge-queue" && rest[1] == "branches" && rest[3] == "settings" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, a.resolve(repository, rest[2]))
		return
	}
	if len(rest) < 2 || len(rest) > 3 || rest[0] != "merge-queue" || rest[1] != "policies" {
		notFound()
		return
	}

	if len(rest) == 2 {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
			return
		}
		body, problem := decodeStrict(r, "name", "enforcement", "target", "settings")
		var policy MergeQueuePolicy
		if problem == "" {
			raw, _ := json.Marshal(body)
			if err := json.Unmarshal(raw, &policy); err != nil {
				problem = err.Error()
			}
		}
		if problem != "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": problem})
			return
		}
		if policy.Enforcement == "" {
			policy.Enforcement = "active"
		}
		if policy.Target.Branches.ExcludePatterns == nil {
			policy.Target.Branches.ExcludePatterns = []string{}
		}
		if !a.validate(w, repository, "", policy) {
			return
		}
		held := 0
		for _, other := range a.policies {
			if other.repository == repository {
				held++
			}
		}
		if held >= a.maxPolicies {
			writeAPIError(w, http.StatusUnprocessableEntity, "MERGE_QUEUE_POLICY_LIMIT_REACHED",
				fmt.Sprintf("An organization, and a repository, hold at most %d merge queue policies.", a.maxPolicies))
			return
		}
		id := a.add(repository, policy)
		writeJSON(w, http.StatusCreated, a.policies[id].body)
		return
	}

	stored, ok := a.policies[rest[2]]
	if !ok || stored.repository != repository {
		writeAPIError(w, http.StatusNotFound, "MERGE_QUEUE_POLICY_NOT_FOUND", "There is no merge queue policy with this id here.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, stored.body)
	case http.MethodPatch:
		body, problem := decodeStrict(r, "name", "enforcement", "target", "settings")
		if problem != "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": problem})
			return
		}
		patched := stored.body
		for field, raw := range body {
			if string(raw) == "null" {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": field + ": cannot be null"})
				return
			}
			var err error
			switch field {
			case "name":
				err = json.Unmarshal(raw, &patched.Name)
			case "enforcement":
				err = json.Unmarshal(raw, &patched.Enforcement)
			case "target":
				var target MergeQueuePolicyTarget
				err = json.Unmarshal(raw, &target)
				patched.Target = target
			case "settings":
				var settings map[string]*bool
				err = json.Unmarshal(raw, &settings)
				for key, value := range settings {
					if key != "batch_pull_request" {
						err = fmt.Errorf("settings: extra input %s", key)
					}
					patched.Settings.BatchPullRequest = value
				}
			}
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
				return
			}
		}
		if !a.validate(w, repository, stored.body.ID, patched) {
			return
		}
		stored.body = patched
		writeJSON(w, http.StatusOK, stored.body)
	case http.MethodDelete:
		delete(a.policies, stored.body.ID)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
	}
}

// checkFake checks what the fake API holds.
func checkFake(api *fakeMergeQueuePoliciesAPI, check func() error) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		return check()
	}
}

// checkOnlyPolicy checks the one policy the fake API holds.
func checkOnlyPolicy(api *fakeMergeQueuePoliciesAPI, check func(*fakePolicy) error) resource.TestCheckFunc {
	return checkFake(api, func() error {
		p, err := api.only()
		if err != nil {
			return err
		}
		return check(p)
	})
}

func checkRequested(api *fakeMergeQueuePoliciesAPI, pattern string) resource.TestCheckFunc {
	re := regexp.MustCompile("^" + pattern + "$")
	return checkFake(api, func() error {
		if !slices.ContainsFunc(api.requests, re.MatchString) {
			return fmt.Errorf("no request matching %s among %v", pattern, api.requests)
		}
		return nil
	})
}

func checkNoPolicy(api *fakeMergeQueuePoliciesAPI) resource.TestCheckFunc {
	return checkFake(api, func() error {
		if len(api.policies) != 0 {
			return fmt.Errorf("%d policies left, want none", len(api.policies))
		}
		return nil
	})
}

// importID returns the import ID of a policy resource of the state.
func importID(name, prefix string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return "", fmt.Errorf("no %s in the state", name)
		}
		return prefix + ":" + rs.Primary.ID, nil
	}
}

func organizationPolicyConfig(srv *httptest.Server, attributes string) string {
	return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_organization_merge_queue_policy" "test" {
  owner = "acme"
  %s
}
`, attributes)
}

const organizationPolicy = `
  name = "Batches without drafts"
  target = {
    repositories = {
      include_patterns = ["~ALL"]
      exclude_patterns = ["docs"]
    }
    branches = {
      include_patterns = ["~DEFAULT_BRANCH", "release/*"]
    }
  }
  settings = {
    batch_pull_request = false
  }
`

func TestOrganizationMergeQueuePolicy(t *testing.T) {
	api, srv := newFakeMergeQueuePoliciesAPI(t)
	name := "mergify_organization_merge_queue_policy.test"
	var id string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(
			checkNoPolicy(api),
			checkRequested(api, "DELETE /orgs/acme/merge-queue/policies/[-0-9a-f]+"),
		),
		Steps: []resource.TestStep{
			{
				Config: organizationPolicyConfig(srv, organizationPolicy),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrWith(name, "id", func(value string) error {
						id = value
						return nil
					}),
					resource.TestCheckResourceAttr(name, "enforcement", "active"),
					resource.TestCheckResourceAttr(name, "settings.batch_pull_request", "false"),
					resource.TestCheckTypeSetElemAttr(name, "target.repositories.include_patterns.*", "~ALL"),
					resource.TestCheckTypeSetElemAttr(name, "target.repositories.exclude_patterns.*", "docs"),
					resource.TestCheckResourceAttr(name, "target.branches.include_patterns.#", "2"),
					resource.TestCheckResourceAttr(name, "target.branches.exclude_patterns.#", "0"),
					checkRequested(api, "POST /orgs/acme/merge-queue/policies"),
					checkOnlyPolicy(api, func(p *fakePolicy) error {
						got := p.body
						switch {
						case p.repository != "":
							return fmt.Errorf("held by %s, want the organization", p.repository)
						case got.Name != "Batches without drafts" || got.Enforcement != "active":
							return fmt.Errorf("got %+v", got)
						case got.Settings.BatchPullRequest == nil || *got.Settings.BatchPullRequest:
							return fmt.Errorf("batch_pull_request: got %v, want false", got.Settings.BatchPullRequest)
						case !slices.Equal(got.Target.Repositories.ExcludePatterns, []string{"docs"}):
							return fmt.Errorf("repositories: got %+v", got.Target.Repositories)
						}
						return nil
					}),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateIdFunc: importID(name, "acme"),
				ImportStateVerify: true,
			},
			{
				// Every field changes in place, through a PATCH of the same
				// policy.
				Config: organizationPolicyConfig(srv, `
  name        = "Drafts back on"
  enforcement = "disabled"
  target = {
    repositories = {
      include_patterns = ["engine"]
    }
    branches = {
      include_patterns = ["~ALL"]
      exclude_patterns = ["main"]
    }
  }
  settings = {
    batch_pull_request = true
  }
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrWith(name, "id", func(value string) error {
						if value != id {
							return fmt.Errorf("id: got %s, want %s: the policy was replaced", value, id)
						}
						return nil
					}),
					resource.TestCheckResourceAttr(name, "enforcement", "disabled"),
					resource.TestCheckResourceAttr(name, "target.repositories.exclude_patterns.#", "0"),
					checkRequested(api, "PATCH /orgs/acme/merge-queue/policies/[-0-9a-f]+"),
					checkOnlyPolicy(api, func(p *fakePolicy) error {
						got := p.body
						switch {
						case got.Name != "Drafts back on" || got.Enforcement != "disabled":
							return fmt.Errorf("got %+v", got)
						case !*got.Settings.BatchPullRequest:
							return fmt.Errorf("batch_pull_request: got false, want true")
						case len(got.Target.Repositories.ExcludePatterns) != 0:
							return fmt.Errorf("repositories: got %+v", got.Target.Repositories)
						case !slices.Equal(got.Target.Branches.ExcludePatterns, []string{"main"}):
							return fmt.Errorf("branches: got %+v", got.Target.Branches)
						}
						return nil
					}),
				),
			},
			{
				// A change made outside Terraform shows in the plan.
				PreConfig: func() {
					api.set(func() {
						p, _ := api.only()
						p.body.Settings.BatchPullRequest = boolPointer(false)
					})
				},
				Config: organizationPolicyConfig(srv, `
  name        = "Drafts back on"
  enforcement = "disabled"
  target = {
    repositories = {
      include_patterns = ["engine"]
    }
    branches = {
      include_patterns = ["~ALL"]
      exclude_patterns = ["main"]
    }
  }
  settings = {
    batch_pull_request = true
  }
`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// A policy deleted outside Terraform is created again.
				PreConfig: func() {
					api.set(func() { api.policies = map[string]*fakePolicy{} })
				},
				Config: organizationPolicyConfig(srv, organizationPolicy),
				Check: checkOnlyPolicy(api, func(p *fakePolicy) error {
					if p.body.ID == id {
						return fmt.Errorf("id %s reused", id)
					}
					return nil
				}),
			},
		},
	})
}

func repositoryPolicyConfig(srv *httptest.Server, repository, attributes string) string {
	return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_repository_merge_queue_policy" "test" {
  owner      = "acme"
  repository = %q
  %s
}
`, repository, attributes)
}

const repositoryPolicy = `
  name = "main without drafts"
  target = {
    branches = {
      include_patterns = ["main"]
    }
  }
  settings = {
    batch_pull_request = false
  }
`

func TestRepositoryMergeQueuePolicy(t *testing.T) {
	api, srv := newFakeMergeQueuePoliciesAPI(t)
	name := "mergify_repository_merge_queue_policy.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(
			checkNoPolicy(api),
			checkRequested(api, "DELETE /orgs/acme/repositories/docs/merge-queue/policies/[-0-9a-f]+"),
		),
		Steps: []resource.TestStep{
			{
				Config: repositoryPolicyConfig(srv, "engine", repositoryPolicy),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "enforcement", "active"),
					resource.TestCheckResourceAttr(name, "target.branches.include_patterns.0", "main"),
					resource.TestCheckNoResourceAttr(name, "target.repositories"),
					checkRequested(api, "POST /orgs/acme/repositories/engine/merge-queue/policies"),
					checkOnlyPolicy(api, func(p *fakePolicy) error {
						if p.repository != "engine" || p.body.Target.Repositories != nil {
							return fmt.Errorf("got %s %+v, want engine's, with no repositories", p.repository, p.body.Target)
						}
						return nil
					}),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateIdFunc: importID(name, "acme/engine"),
				ImportStateVerify: true,
			},
			{
				// Another repository is another policy: the first one is
				// deleted, not left behind.
				Config: repositoryPolicyConfig(srv, "docs", repositoryPolicy),
				Check: resource.ComposeTestCheckFunc(
					checkRequested(api, "DELETE /orgs/acme/repositories/engine/merge-queue/policies/[-0-9a-f]+"),
					checkOnlyPolicy(api, func(p *fakePolicy) error {
						if p.repository != "docs" {
							return fmt.Errorf("held by %s, want docs", p.repository)
						}
						return nil
					}),
				),
			},
			{
				// Mergify no longer knows the repository: the policy is
				// planned for creation again.
				PreConfig: func() {
					api.set(func() {
						api.repos = slices.DeleteFunc(api.repos, func(r string) bool { return r == "docs" })
					})
				},
				Config:             repositoryPolicyConfig(srv, "docs", repositoryPolicy),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				PreConfig: func() { api.set(func() { api.repos = append(api.repos, "docs") }) },
				Config:    repositoryPolicyConfig(srv, "docs", repositoryPolicy),
			},
		},
	})
}

func TestMergeQueuePolicyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*fakeMergeQueuePoliciesAPI)
		config string
		want   string
	}{
		{
			name:   "false not available",
			setup:  func(api *fakeMergeQueuePoliciesAPI) { api.falseAvailable = false },
			config: repositoryPolicy,
			want:   `batch_pull_request = false is not available to this organization.*Contact\s+Mergify\s+Support`,
		},
		{
			name: "invalid pattern",
			config: `
  name = "main"
  target = { branches = { include_patterns = ["~MAIN"] } }
  settings = { batch_pull_request = false }
`,
			want: `Invalid merge queue policy target pattern.*~MAIN`,
		},
		{
			name: "name taken",
			setup: func(api *fakeMergeQueuePoliciesAPI) {
				api.add("engine", MergeQueuePolicy{
					Name:     "main without drafts",
					Target:   MergeQueuePolicyTarget{Branches: MergeQueuePolicyPatterns{IncludePatterns: []string{"~ALL"}, ExcludePatterns: []string{}}},
					Settings: MergeQueuePolicySettings{BatchPullRequest: boolPointer(true)},
				})
			},
			config: repositoryPolicy,
			want:   `Merge queue policy name already taken.*already\s+named`,
		},
		{
			name:   "limit reached",
			setup:  func(api *fakeMergeQueuePoliciesAPI) { api.maxPolicies = 0 },
			config: repositoryPolicy,
			want:   `Too many merge queue policies.*at most 0`,
		},
		{
			name: "no setting",
			config: `
  name = "main"
  target = { branches = { include_patterns = ["main"] } }
  settings = {}
`,
			want: `No setting set`,
		},
		{
			name: "no branch",
			config: `
  name = "main"
  target = { branches = { include_patterns = [] } }
  settings = { batch_pull_request = false }
`,
			want: `Empty set`,
		},
		{
			name: "invalid enforcement",
			config: `
  name = "main"
  enforcement = "evaluate"
  target = { branches = { include_patterns = ["main"] } }
  settings = { batch_pull_request = false }
`,
			want: "Invalid value.*must be one of `active`, `disabled`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, srv := newFakeMergeQueuePoliciesAPI(t)
			if tc.setup != nil {
				tc.setup(api)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories(),
				Steps: []resource.TestStep{
					{
						Config:      repositoryPolicyConfig(srv, "engine", tc.config),
						ExpectError: regexp.MustCompile("(?s)" + tc.want),
					},
				},
			})
		})
	}
}

func TestMergeQueuePolicyImportID(t *testing.T) {
	const id = "0b7e4c1a-1f0e-4a8e-9c52-5d1c1d0a2b3c"
	for _, tc := range []struct {
		holder mergeQueuePolicyHolder
		id     string
		want   *MergeQueuePolicyScope
	}{
		{organizationHolder, "acme:" + id, &MergeQueuePolicyScope{Owner: "acme"}},
		{organizationHolder, "acme", nil},
		{organizationHolder, "acme:", nil},
		{organizationHolder, ":" + id, nil},
		{organizationHolder, "acme/engine:" + id, nil},
		{repositoryHolder, "acme/engine:" + id, &MergeQueuePolicyScope{Owner: "acme", Repository: "engine"}},
		{repositoryHolder, "acme:" + id, nil},
		{repositoryHolder, "acme/:" + id, nil},
		{repositoryHolder, "acme/engine/main:" + id, nil},
		{repositoryHolder, "acme/engine:" + id + "/x", nil},
	} {
		scope, policyID, ok := tc.holder.parseImportID(tc.id)
		switch {
		case tc.want == nil && ok:
			t.Errorf("%s %q: got %+v, want refused", tc.holder.typeSuffix(), tc.id, scope)
		case tc.want != nil && (!ok || scope != *tc.want || policyID != id):
			t.Errorf("%s %q: got %+v %q (%v), want %+v", tc.holder.typeSuffix(), tc.id, scope, policyID, ok, *tc.want)
		}
	}
}

const branchSettingsConfig = `
resource "mergify_organization_merge_queue_policy" "no_drafts" {
  owner = "acme"
  name  = "No drafts on default branches"
  target = {
    repositories = { include_patterns = ["~ALL"], exclude_patterns = ["docs"] }
    branches     = { include_patterns = ["~DEFAULT_BRANCH", "release/*"] }
  }
  settings = { batch_pull_request = false }
}

resource "mergify_repository_merge_queue_policy" "release_drafts" {
  owner      = "acme"
  repository = "engine"
  name       = "Drafts on release branches"
  target     = { branches = { include_patterns = ["release/*"] } }
  settings   = { batch_pull_request = true }
}

data "mergify_merge_queue_branch_settings" "main" {
  owner      = "acme"
  repository = "engine"
  branch     = "main"
  depends_on = [mergify_organization_merge_queue_policy.no_drafts, mergify_repository_merge_queue_policy.release_drafts]
}

data "mergify_merge_queue_branch_settings" "release" {
  owner      = "acme"
  repository = "engine"
  branch     = "release/1.0"
  depends_on = [mergify_organization_merge_queue_policy.no_drafts, mergify_repository_merge_queue_policy.release_drafts]
}

data "mergify_merge_queue_branch_settings" "docs" {
  owner      = "acme"
  repository = "docs"
  branch     = "main"
  depends_on = [mergify_organization_merge_queue_policy.no_drafts, mergify_repository_merge_queue_policy.release_drafts]
}
`

func TestMergeQueueBranchSettingsDataSource(t *testing.T) {
	_, srv := newFakeMergeQueuePoliciesAPI(t)
	main := "data.mergify_merge_queue_branch_settings.main"
	release := "data.mergify_merge_queue_branch_settings.release"
	docs := "data.mergify_merge_queue_branch_settings.docs"
	organization := "mergify_organization_merge_queue_policy.no_drafts"
	repository := "mergify_repository_merge_queue_policy.release_drafts"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv) + branchSettingsConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(main, "id", "acme/engine:main"),
					resource.TestCheckResourceAttr(main, "settings.batch_pull_request", "false"),
					resource.TestCheckResourceAttr(main, "decisions.batch_pull_request.rule", "policies_agree"),
					resource.TestCheckResourceAttrPair(main, "decisions.batch_pull_request.policy_ids.0", organization, "id"),
					resource.TestCheckResourceAttr(main, "policies.#", "1"),
					resource.TestCheckResourceAttrPair(main, "policies.0.id", organization, "id"),
					resource.TestCheckResourceAttr(main, "policies.0.name", "No drafts on default branches"),
					resource.TestCheckNoResourceAttr(main, "policies.0.repository"),
					resource.TestCheckTypeSetElemAttr(main, "policies.0.target.repositories.exclude_patterns.*", "docs"),
					resource.TestCheckResourceAttr(main, "policies.0.settings.batch_pull_request", "false"),

					// The policies disagree: the safest value, a draft, wins.
					resource.TestCheckResourceAttr(release, "settings.batch_pull_request", "true"),
					resource.TestCheckResourceAttr(release, "decisions.batch_pull_request.rule", "policies_disagree"),
					resource.TestCheckResourceAttr(release, "decisions.batch_pull_request.policy_ids.#", "1"),
					resource.TestCheckResourceAttrPair(release, "decisions.batch_pull_request.policy_ids.0", repository, "id"),
					resource.TestCheckResourceAttr(release, "policies.#", "2"),

					resource.TestCheckResourceAttr(docs, "settings.batch_pull_request", "true"),
					resource.TestCheckResourceAttr(docs, "decisions.batch_pull_request.rule", "default"),
					resource.TestCheckResourceAttr(docs, "decisions.batch_pull_request.policy_ids.#", "0"),
					resource.TestCheckResourceAttr(docs, "policies.#", "0"),
				),
			},
		},
	})
}
