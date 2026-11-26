package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeTrustPoliciesAPI mimics the OIDC trust policy routes of the Mergify API
// for one organization.
type fakeTrustPoliciesAPI struct {
	mu    sync.Mutex
	owner string
	// The ids of the organization's repositories Mergify's GitHub App can
	// access.
	repositoryIDs []int64
	maxPolicies   int
	// Whether the caller is an organization admin signed in as themselves,
	// rather than an application key.
	admin bool
	// Whether the API predates trust policy permissions: it refuses the
	// field, and leaves it out of its answers.
	withoutPermissions bool
	policies           map[string]*TrustPolicy
	created            int
	// Every request, as `<method> <path>`, and every write the fake could
	// decode.
	requests []string
	writes   []fakeTrustPolicyWrite
}

type fakeTrustPolicyWrite struct {
	method string
	body   map[string]json.RawMessage
}

func newFakeTrustPoliciesAPI(t *testing.T) (*fakeTrustPoliciesAPI, *httptest.Server) {
	api := &fakeTrustPoliciesAPI{
		owner:         "acme",
		repositoryIDs: []int64{1001, 1002, 1003},
		maxPolicies:   100,
		policies:      map[string]*TrustPolicy{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, srv
}

func (a *fakeTrustPoliciesAPI) set(update func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	update()
}

func (a *fakeTrustPoliciesAPI) defaultConditions() GitHubActionsConditions {
	return GitHubActionsConditions{Events: slices.Clone(ciEvents)}
}

func (a *fakeTrustPoliciesAPI) defaultPermissions() map[string]string {
	if a.withoutPermissions {
		return nil
	}
	return maps.Clone(ciOnlyPermissions)
}

// add stores a policy as if created outside Terraform, and returns its id.
func (a *fakeTrustPoliciesAPI) add(policy TrustPolicy) string {
	a.created++
	policy.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", a.created)
	a.policies[policy.ID] = &policy
	return policy.ID
}

// only returns the one policy the fake holds.
func (a *fakeTrustPoliciesAPI) only() (*TrustPolicy, error) {
	if len(a.policies) != 1 {
		return nil, fmt.Errorf("%d policies, want 1", len(a.policies))
	}
	for _, p := range a.policies {
		return p, nil
	}
	return nil, nil
}

// decodeTrustPolicy reads the policy a write sends. The provider sends every field
// but `permissions`, which a policy written without keeps permissions.
func decodeTrustPolicy(body map[string]json.RawMessage, permissions map[string]string) (*TrustPolicy, string) {
	raw, _ := json.Marshal(body)
	var policy TrustPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return nil, err.Error()
	}
	if _, sent := body["permissions"]; !sent {
		policy.Permissions = maps.Clone(permissions)
	}
	return &policy, ""
}

func beyondCI(permissions map[string]string) bool {
	for grantee := range permissions {
		if grantee != "ci_reporting" {
			return true
		}
	}
	return false
}

// validate checks a policy as the API does before writing it over stored,
// nil for a new one, and answers the refusal when there is one.
func (a *fakeTrustPoliciesAPI) validate(w http.ResponseWriter, stored, policy *TrustPolicy) bool {
	unprocessable := func(detail string) bool {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": detail})
		return false
	}
	repositories := policy.Repositories
	conditions := policy.Conditions
	switch {
	case policy.Name == "" || policy.Provider != githubActionsProvider:
		return unprocessable("name and provider: Field required")
	case !slices.Contains(repositoriesModes, repositories.Mode),
		(repositories.Mode == "only" || repositories.Mode == "all_except") != (len(repositories.IDs) > 0),
		(repositories.Mode == "visibility") != (len(repositories.Visibilities) > 0):
		return unprocessable("repositories: does not match any of the allowed modes")
	case conditions.Events != nil && len(conditions.Events) == 0,
		conditions.Workflows != nil && len(conditions.Workflows) == 0,
		conditions.Environments != nil && len(conditions.Environments) == 0:
		return unprocessable("conditions: List should have at least 1 item")
	}
	named := slices.Clone(repositories.IDs)
	for _, workflow := range conditions.Workflows {
		if workflow.RepositoryID != nil {
			named = append(named, *workflow.RepositoryID)
		} else if workflow.Ref != nil {
			return unprocessable("conditions.workflows: Extra inputs are not permitted: ref")
		}
	}
	for _, id := range named {
		if !slices.Contains(a.repositoryIDs, id) {
			writeAPIError(w, http.StatusUnprocessableEntity, "CI_TRUST_POLICY_REPOSITORY_UNKNOWN",
				fmt.Sprintf("%s has no repository Mergify's GitHub App can access with the id %d.", a.owner, id))
			return false
		}
	}
	for otherID, other := range a.policies {
		if (stored == nil || otherID != stored.ID) && other.Name == policy.Name {
			writeAPIError(w, http.StatusConflict, "CI_TRUST_POLICY_NAME_TAKEN",
				fmt.Sprintf("Another trust policy is already named %q. Pick another name, or edit that policy instead.", policy.Name))
			return false
		}
	}
	if !a.admin && (beyondCI(policy.Permissions) || (stored != nil && beyondCI(stored.Permissions))) {
		a.refuseGrant(w)
		return false
	}
	return true
}

// refuse answers a 422, shaped as the API's when a body holds a field it
// does not know.
func (a *fakeTrustPoliciesAPI) refuse(w http.ResponseWriter, problem string) {
	if field, extra := strings.CutPrefix(problem, "Extra inputs are not permitted: "); extra {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []map[string]any{
			{"type": "extra_forbidden", "loc": []string{"body", field}, "msg": "Extra inputs are not permitted"},
		}})
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": problem})
}

func (a *fakeTrustPoliciesAPI) refuseGrant(w http.ResponseWriter) {
	writeAPIError(w, http.StatusForbidden, "CI_TRUST_POLICY_GRANT_REFUSED",
		"An application key writes only the trust policies that grant CI Reporting alone. An organization admin can "+
			"write this one, signed in as themselves, in the Mergify dashboard or with the mergify CLI.")
}

func (a *fakeTrustPoliciesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.EscapedPath())

	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Unauthorized"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || len(parts) > 5 || parts[0] != "ci" || parts[1] != a.owner || parts[2] != "oidc" || parts[3] != "trust-policies" {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
		return
	}
	fields := []string{"name", "provider", "repositories", "conditions"}
	if !a.withoutPermissions {
		fields = append(fields, "permissions")
	}

	if len(parts) == 4 {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
			return
		}
		body, problem := decodeStrict(r, fields...)
		var policy *TrustPolicy
		if problem == "" {
			a.writes = append(a.writes, fakeTrustPolicyWrite{r.Method, body})
			policy, problem = decodeTrustPolicy(body, a.defaultPermissions())
		}
		if problem != "" {
			a.refuse(w, problem)
			return
		}
		if !a.validate(w, nil, policy) {
			return
		}
		if len(a.policies) >= a.maxPolicies {
			writeAPIError(w, http.StatusUnprocessableEntity, "CI_TRUST_POLICY_LIMIT_REACHED",
				fmt.Sprintf("%s already has %d trust policies, the most an organization can have.", a.owner, a.maxPolicies))
			return
		}
		id := a.add(*policy)
		writeJSON(w, http.StatusCreated, a.policies[id])
		return
	}

	stored, ok := a.policies[parts[4]]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "CI_TRUST_POLICY_NOT_FOUND", a.owner+" has no trust policy with this id.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, stored)
	case http.MethodPatch:
		body, problem := decodeStrict(r, fields...)
		var patched *TrustPolicy
		if problem == "" {
			a.writes = append(a.writes, fakeTrustPolicyWrite{r.Method, body})
			patched, problem = decodeTrustPolicy(body, stored.Permissions)
		}
		if problem != "" {
			a.refuse(w, problem)
			return
		}
		// What the policy holds already records no edit.
		patched.ID = stored.ID
		if !reflect.DeepEqual(patched, stored) {
			if !a.validate(w, stored, patched) {
				return
			}
			a.policies[stored.ID] = patched
		}
		writeJSON(w, http.StatusOK, a.policies[stored.ID])
	case http.MethodDelete:
		if !a.admin && beyondCI(stored.Permissions) {
			a.refuseGrant(w)
			return
		}
		delete(a.policies, stored.ID)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
	}
}

func checkTrustPolicies(api *fakeTrustPoliciesAPI, check func() error) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		return check()
	}
}

// checkOnlyTrustPolicy checks the one policy the fake API holds.
func checkOnlyTrustPolicy(api *fakeTrustPoliciesAPI, check func(*TrustPolicy) error) resource.TestCheckFunc {
	return checkTrustPolicies(api, func() error {
		p, err := api.only()
		if err != nil {
			return err
		}
		return check(p)
	})
}

// checkLastWrite checks the method and the body of the last write the
// fake API was sent.
func checkLastWrite(api *fakeTrustPoliciesAPI, method string, check func(map[string]json.RawMessage) error) resource.TestCheckFunc {
	return checkTrustPolicies(api, func() error {
		if len(api.writes) == 0 {
			return fmt.Errorf("no write among %v", api.requests)
		}
		last := api.writes[len(api.writes)-1]
		if last.method != method {
			return fmt.Errorf("last write: got a %s, want a %s", last.method, method)
		}
		return check(last.body)
	})
}

func sendsPermissions(want string) func(map[string]json.RawMessage) error {
	return func(body map[string]json.RawMessage) error {
		got, sent := body["permissions"]
		switch {
		case want == "" && sent:
			return fmt.Errorf("permissions: got %s, want left out", got)
		case want != "" && string(got) != want:
			return fmt.Errorf("permissions: got %q, want %s", got, want)
		}
		return nil
	}
}

func checkNoTrustPolicy(api *fakeTrustPoliciesAPI) resource.TestCheckFunc {
	return checkTrustPolicies(api, func() error {
		if len(api.policies) != 0 {
			return fmt.Errorf("%d policies left, want none", len(api.policies))
		}
		if !slices.ContainsFunc(api.requests, regexp.MustCompile("^DELETE /ci/acme/oidc/trust-policies/[-0-9a-f]+$").MatchString) {
			return fmt.Errorf("no DELETE among %v", api.requests)
		}
		return nil
	})
}

func trustPolicyConfig(srv *httptest.Server, attributes string) string {
	return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_oidc_trust_policy" "test" {
  owner = "acme"
  %s
}
`, attributes)
}

const trustPolicyDefaults = `
  name           = "CI on every repository"
  github_actions = {}
`

const trustPolicyEveryField = `
  name = "Deploys"
  repositories = {
    mode = "only"
    ids  = [1001, 1002]
  }
  github_actions = {
    events = ["push", "workflow_dispatch"]
    workflows = [
      { path = ".github/workflows/deploy.yml" },
      { repository_id = 1003, path = ".github/workflows/release.yml", ref = "refs/heads/main" },
    ]
    environments = ["production"]
  }
`

const trustPolicyResource = "mergify_oidc_trust_policy.test"

func TestOIDCTrustPolicy(t *testing.T) {
	api, srv := newFakeTrustPoliciesAPI(t)
	name := trustPolicyResource
	var id string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkNoTrustPolicy(api),
		Steps: []resource.TestStep{
			{
				Config: trustPolicyConfig(srv, trustPolicyDefaults),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrWith(name, "id", func(value string) error {
						id = value
						return nil
					}),
					resource.TestCheckResourceAttr(name, "repositories.mode", "all"),
					resource.TestCheckNoResourceAttr(name, "repositories.ids.#"),
					resource.TestCheckResourceAttr(name, "github_actions.events.#", "5"),
					resource.TestCheckTypeSetElemAttr(name, "github_actions.events.*", "merge_group"),
					resource.TestCheckNoResourceAttr(name, "github_actions.workflows.#"),
					resource.TestCheckNoResourceAttr(name, "github_actions.environments.#"),
					resource.TestCheckResourceAttr(name, "permissions.%", "1"),
					resource.TestCheckResourceAttr(name, "permissions.ci_reporting", "write"),
					// A CI-only policy leaves its permissions out, which a
					// Mergify predating them takes too.
					checkLastWrite(api, http.MethodPost, sendsPermissions("")),
					checkOnlyTrustPolicy(api, func(p *TrustPolicy) error {
						c := p.Conditions
						switch {
						case p.Name != "CI on every repository" || p.Provider != githubActionsProvider || p.Repositories.Mode != "all":
							return fmt.Errorf("got %+v", p)
						case !sameElements(c.Events, ciEvents):
							return fmt.Errorf("events: got %+v, want the CI events", c.Events)
						case c.Workflows != nil || c.Environments != nil:
							return fmt.Errorf("workflows and environments: got %+v %+v, want any", c.Workflows, c.Environments)
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
				Config: trustPolicyConfig(srv, trustPolicyEveryField),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrWith(name, "id", func(value string) error {
						if value != id {
							return fmt.Errorf("id: got %s, want %s: the policy was replaced", value, id)
						}
						return nil
					}),
					resource.TestCheckResourceAttr(name, "github_actions.workflows.#", "2"),
					// An update sends permissions whatever the policy granted when
					// planned.
					checkLastWrite(api, http.MethodPatch, sendsPermissions(`{"ci_reporting":"write"}`)),
					checkOnlyTrustPolicy(api, func(p *TrustPolicy) error {
						c := p.Conditions
						workflows := []TrustPolicyWorkflow{
							{Path: ".github/workflows/deploy.yml"},
							{RepositoryID: pointer(int64(1003)), Path: ".github/workflows/release.yml", Ref: pointer("refs/heads/main")},
						}
						switch {
						case p.Name != "Deploys" || p.Repositories.Mode != "only" || !sameElements(p.Repositories.IDs, []int64{1001, 1002}):
							return fmt.Errorf("got %+v", p)
						case !sameElements(c.Events, []string{"push", "workflow_dispatch"}):
							return fmt.Errorf("events: got %+v", c.Events)
						case len(c.Workflows) != 2 || !slices.ContainsFunc(c.Workflows, func(w TrustPolicyWorkflow) bool { return reflect.DeepEqual(w, workflows[0]) }) ||
							!slices.ContainsFunc(c.Workflows, func(w TrustPolicyWorkflow) bool { return reflect.DeepEqual(w, workflows[1]) }):
							return fmt.Errorf("workflows: got %+v", c.Workflows)
						case !slices.Equal(c.Environments, []string{"production"}):
							return fmt.Errorf("environments: got %+v", c.Environments)
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
				// A change made outside Terraform shows in the plan.
				PreConfig: func() {
					api.set(func() {
						p, _ := api.only()
						p.Conditions.Events = nil
					})
				},
				Config:             trustPolicyConfig(srv, trustPolicyEveryField),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Left out of the configuration, a field takes its default
				// back.
				Config: trustPolicyConfig(srv, trustPolicyDefaults),
				Check: checkOnlyTrustPolicy(api, func(p *TrustPolicy) error {
					c := p.Conditions
					switch {
					case p.Repositories.Mode != "all" || p.Repositories.IDs != nil:
						return fmt.Errorf("repositories: got %+v", p.Repositories)
					case !sameElements(c.Events, ciEvents):
						return fmt.Errorf("events: got %+v, want the CI events", c.Events)
					case c.Workflows != nil || c.Environments != nil:
						return fmt.Errorf("workflows and environments: got %+v %+v, want any", c.Workflows, c.Environments)
					}
					return nil
				}),
			},
			{
				Config: trustPolicyConfig(srv, `
  name = "CI on private repositories"
  repositories = {
    mode         = "visibility"
    visibilities = ["private", "internal"]
  }
  github_actions = {
    events = ["any"]
  }
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "github_actions.events.#", "1"),
					resource.TestCheckTypeSetElemAttr(name, "github_actions.events.*", "any"),
					checkLastWrite(api, http.MethodPatch, func(body map[string]json.RawMessage) error {
						if want := `"events":"any"`; !strings.Contains(string(body["conditions"]), want) {
							return fmt.Errorf("conditions: got %s, want %s", body["conditions"], want)
						}
						return nil
					}),
					checkOnlyTrustPolicy(api, func(p *TrustPolicy) error {
						if p.Conditions.Events != nil || p.Repositories.Mode != "visibility" || !sameElements(p.Repositories.Visibilities, []string{"internal", "private"}) {
							return fmt.Errorf("got %+v", p)
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
				// A policy deleted outside Terraform is created again.
				PreConfig: func() {
					api.set(func() { api.policies = map[string]*TrustPolicy{} })
				},
				Config: trustPolicyConfig(srv, trustPolicyDefaults),
				Check: checkOnlyTrustPolicy(api, func(p *TrustPolicy) error {
					if p.ID == id {
						return fmt.Errorf("id %s reused", id)
					}
					return nil
				}),
			},
		},
	})
}

func TestOIDCTrustPolicyPermissions(t *testing.T) {
	api, srv := newFakeTrustPoliciesAPI(t)
	api.admin = true
	name := trustPolicyResource
	const granting = `
  name           = "Merge queue pauses"
  github_actions = { environments = ["production"] }
  permissions    = { ci_reporting = "write", merge_queue_pause = "write" }
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkNoTrustPolicy(api),
		Steps: []resource.TestStep{
			{
				Config: trustPolicyConfig(srv, granting),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "permissions.%", "2"),
					resource.TestCheckResourceAttr(name, "permissions.merge_queue_pause", "write"),
					checkLastWrite(api, http.MethodPost, sendsPermissions(`{"ci_reporting":"write","merge_queue_pause":"write"}`)),
				),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateIdFunc: importID(name, "acme"),
				ImportStateVerify: true,
			},
			{
				Config: trustPolicyConfig(srv, `
  name           = "Merge queue pauses"
  github_actions = { environments = ["production"] }
  permissions    = { "*" = "read", ci_reporting = "write" }
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "permissions.*", "read"),
					checkLastWrite(api, http.MethodPatch, sendsPermissions(`{"*":"read","ci_reporting":"write"}`)),
				),
			},
			{
				// An application key changes nothing of a policy granting
				// more than CI reporting.
				PreConfig: func() { api.set(func() { api.admin = false }) },
				Config: trustPolicyConfig(srv, `
  name           = "Renamed"
  github_actions = { environments = ["production"] }
  permissions    = { "*" = "read", ci_reporting = "write" }
`),
				ExpectError: regexp.MustCompile(`(?s)Trust policy permissions refused.*An application key writes only`),
			},
			{
				// Left out of the configuration, permissions take CI reporting
				// alone back, which the PATCH says: the policy grants more.
				PreConfig: func() { api.set(func() { api.admin = true }) },
				Config: trustPolicyConfig(srv, `
  name           = "Merge queue pauses"
  github_actions = { environments = ["production"] }
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "permissions.%", "1"),
					checkLastWrite(api, http.MethodPatch, sendsPermissions(`{"ci_reporting":"write"}`)),
					checkOnlyTrustPolicy(api, func(p *TrustPolicy) error {
						if !maps.Equal(p.Permissions, ciOnlyPermissions) {
							return fmt.Errorf("permissions: got %v, want CI reporting alone", p.Permissions)
						}
						return nil
					}),
				),
			},
			{
				// An application key cannot make it grant more, and destroys
				// it once it grants CI reporting alone.
				PreConfig:   func() { api.set(func() { api.admin = false }) },
				Config:      trustPolicyConfig(srv, granting),
				ExpectError: regexp.MustCompile(`(?s)Trust policy permissions refused.*An application key writes only`),
			},
		},
	})
}

func TestOIDCTrustPolicyWithoutPermissions(t *testing.T) {
	api, srv := newFakeTrustPoliciesAPI(t)
	api.withoutPermissions = true
	name := trustPolicyResource
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkNoTrustPolicy(api),
		Steps: []resource.TestStep{
			{
				Config: trustPolicyConfig(srv, trustPolicyDefaults),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "permissions.%", "1"),
					resource.TestCheckResourceAttr(name, "permissions.ci_reporting", "write"),
				),
			},
			{
				Config: trustPolicyConfig(srv, trustPolicyEveryField),
				// Refused with permissions, the PATCH is sent again without.
				Check: checkLastWrite(api, http.MethodPatch, sendsPermissions("")),
			},
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateIdFunc: importID(name, "acme"),
				ImportStateVerify: true,
			},
			{
				Config: trustPolicyConfig(srv, trustPolicyEveryField+`
  permissions = { ci_reporting = "write", merge_queue_pause = "write" }
`),
				ExpectError: regexp.MustCompile(`(?s)Permissions unknown to this Mergify.*predates\s+trust\s+policy\s+permissions`),
			},
		},
	})
}

func TestOIDCTrustPolicyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*fakeTrustPoliciesAPI)
		config string
		want   string
	}{
		{
			name: "name taken",
			setup: func(api *fakeTrustPoliciesAPI) {
				api.add(TrustPolicy{
					Name:         "CI on every repository",
					Provider:     githubActionsProvider,
					Repositories: TrustPolicyRepositories{Mode: "all"},
					Conditions:   api.defaultConditions(),
					Permissions:  api.defaultPermissions(),
				})
			},
			config: trustPolicyDefaults,
			want:   `Trust policy name already taken.*already\s+named`,
		},
		{
			name:   "limit reached",
			setup:  func(api *fakeTrustPoliciesAPI) { api.maxPolicies = 0 },
			config: trustPolicyDefaults,
			want:   `Too many trust policies.*the\s+most`,
		},
		{
			name: "unknown repository",
			config: `
  name           = "CI"
  repositories   = { mode = "all_except", ids = [999] }
  github_actions = {}
`,
			want: `Unknown repository in trust policy.*999`,
		},
		{
			name: "grant by an application key",
			config: `
  name           = "CI"
  github_actions = {}
  permissions    = { ci_reporting = "write", merge_queue_pause = "write" }
`,
			want: `Trust policy permissions refused.*An\s+application\s+key`,
		},
		{
			name: "invalid mode",
			config: `
  name           = "CI"
  repositories   = { mode = "some" }
  github_actions = {}
`,
			want: "Invalid value.*must be one of `all`",
		},
		{
			name: "ids for all",
			config: `
  name           = "CI"
  repositories   = { mode = "all", ids = [1001] }
  github_actions = {}
`,
			want: "Unexpected ids.*The `all` mode takes no `ids`",
		},
		{
			name: "only without ids",
			config: `
  name           = "CI"
  repositories   = { mode = "only" }
  github_actions = {}
`,
			want: "Missing ids",
		},
		{
			name: "visibility without visibilities",
			config: `
  name           = "CI"
  repositories   = { mode = "visibility", ids = [1001] }
  github_actions = {}
`,
			want: "Missing visibilities.*Unexpected ids|Unexpected ids.*Missing visibilities",
		},
		{
			name: "no repository id",
			config: `
  name           = "CI"
  repositories   = { mode = "only", ids = [] }
  github_actions = {}
`,
			want: "Empty set.*List at least one repository id",
		},
		{
			name: "invalid visibility",
			config: `
  name           = "CI"
  repositories   = { mode = "visibility", visibilities = ["secret"] }
  github_actions = {}
`,
			want: "Invalid value.*Got `secret`",
		},
		{
			name: "any with other events",
			config: `
  name           = "CI"
  github_actions = { events = ["any", "push"] }
`,
			want: "Events listed with any",
		},
		{
			name: "no event",
			config: `
  name           = "CI"
  github_actions = { events = [] }
`,
			want: "Empty set.*List at least one event",
		},
		{
			name: "no workflow",
			config: `
  name           = "CI"
  github_actions = { workflows = [] }
`,
			want: "Empty set.*leave\\s+`workflows`\\s+out",
		},
		{
			name: "no environment",
			config: `
  name           = "CI"
  github_actions = { environments = [] }
`,
			want: "Empty set.*leave\\s+`environments`\\s+out",
		},
		{
			name: "ref of an own workflow",
			config: `
  name           = "CI"
  github_actions = { workflows = [{ path = ".github/workflows/ci.yml", ref = "refs/heads/main" }] }
`,
			want: "Ref without repository_id",
		},
		{
			name: "invalid level",
			config: `
  name           = "CI"
  github_actions = {}
  permissions    = { ci_reporting = "admin" }
`,
			want: "Invalid value.*Got `admin`",
		},
		{
			name: "no permission",
			config: `
  name           = "CI"
  github_actions = {}
  permissions    = {}
`,
			want: "No permission",
		},
		{
			name: "permission granted twice",
			config: `
  name           = "CI"
  github_actions = {}
  permissions    = { "*" = "write", merge_queue = "read", ci_reporting = "write" }
`,
			want: "Permission granted twice.*`\\*` grants `merge_queue` at `read`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, srv := newFakeTrustPoliciesAPI(t)
			if tc.setup != nil {
				tc.setup(api)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories(),
				Steps: []resource.TestStep{
					{
						Config:      trustPolicyConfig(srv, tc.config),
						ExpectError: regexp.MustCompile("(?s)" + tc.want),
					},
				},
			})
		})
	}
}

func TestOIDCTrustPolicyOfAnotherProvider(t *testing.T) {
	api, srv := newFakeTrustPoliciesAPI(t)
	id := api.add(TrustPolicy{
		Name:         "Pipelines",
		Provider:     "pipelines",
		Repositories: TrustPolicyRepositories{Mode: "all"},
		Permissions:  api.defaultPermissions(),
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:        trustPolicyConfig(srv, trustPolicyDefaults),
				ResourceName:  trustPolicyResource,
				ImportState:   true,
				ImportStateId: "acme:" + id,
				ExpectError:   regexp.MustCompile("(?s)Trust policy of another provider.*`pipelines`"),
			},
		},
	})
}

func TestTrustPolicyLineRefusesNull(t *testing.T) {
	var conditions GitHubActionsConditions
	err := json.Unmarshal([]byte(`{"events": "any", "workflows": null, "environments": ["production"]}`), &conditions)
	if err == nil || !strings.Contains(err.Error(), "got null") {
		t.Fatalf("got %v, want null refused rather than read as any", err)
	}
}

func TestTrustPolicyValidatorsSkipUnknown(t *testing.T) {
	ctx := t.Context()
	repositories := types.ObjectValueMust(trustPolicyRepositoriesType.AttrTypes, map[string]attr.Value{
		"mode":         types.StringValue("all"),
		"ids":          types.SetUnknown(types.Int64Type),
		"visibilities": types.SetNull(types.StringType),
	})
	var repositoriesResp validator.ObjectResponse
	repositoriesForMode{}.ValidateObject(ctx, validator.ObjectRequest{ConfigValue: repositories}, &repositoriesResp)
	workflow := types.ObjectValueMust(trustPolicyWorkflowType.AttrTypes, map[string]attr.Value{
		"path":          types.StringValue(".github/workflows/ci.yml"),
		"repository_id": types.Int64Null(),
		"ref":           types.StringUnknown(),
	})
	var workflowResp validator.ObjectResponse
	refPinsReusableWorkflow{}.ValidateObject(ctx, validator.ObjectRequest{ConfigValue: workflow}, &workflowResp)
	// A variable defaulting to null is unknown when validated.
	for name, diags := range map[string]diag.Diagnostics{"repositories": repositoriesResp.Diagnostics, "workflow": workflowResp.Diagnostics} {
		if diags.HasError() {
			t.Errorf("%s: got %v, want no error on an unknown value", name, diags)
		}
	}
}
