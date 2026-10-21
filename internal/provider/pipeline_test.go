package provider

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The shapes of the engine's `pipelines/definition.py`, declared apart from
// the provider's so that a field the provider misnames is refused rather than
// echoed back.
type fakePipelineBody struct {
	Name       string                 `json:"name"`
	Triggers   *[]fakePipelineTrigger `json:"triggers,omitzero"`
	InitialJob *fakeInitialJob        `json:"initial_job"`
	Vault      *fakeVault             `json:"vault"`
}

type fakePipelineTrigger struct {
	Context  string   `json:"context"`
	Branches []string `json:"branches,omitzero"`
	Queues   []string `json:"queues,omitzero"`
}

type fakeInitialJob struct {
	Image   string                `json:"image"`
	Args    []string              `json:"args,omitzero"`
	RunsOn  *string               `json:"runs_on,omitzero"`
	Secrets map[string]fakeSecret `json:"secrets,omitzero"`
}

type fakeSecret struct {
	Path  string  `json:"path"`
	Field string  `json:"field"`
	Role  *string `json:"role,omitzero"`
	Env   *bool   `json:"env,omitzero"`
}

type fakeVault struct {
	Address       string  `json:"address"`
	Audience      string  `json:"audience"`
	AuthMount     *string `json:"auth_mount,omitzero"`
	Namespace     *string `json:"namespace,omitzero"`
	CACertificate *string `json:"ca_certificate,omitzero"`
	DefaultRole   *string `json:"default_role,omitzero"`
}

type fakePipeline struct {
	repository string
	definition fakePipelineBody
}

// fakePipelinesAPI mimics the engine's `web/api/pipelines.py` for the
// repositories of one organization.
type fakePipelinesAPI struct {
	mu        sync.Mutex
	owner     string
	repos     []string
	pipelines map[int64]*fakePipeline
	nextID    int64
}

func newFakePipelinesAPI(t *testing.T) (*fakePipelinesAPI, *httptest.Server) {
	api := &fakePipelinesAPI{
		owner:     "acme",
		repos:     []string{"engine", "docs"},
		pipelines: map[int64]*fakePipeline{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, srv
}

func (a *fakePipelinesAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pipelines)
}

func (a *fakePipelinesAPI) update(id int64, change func(*fakePipelineBody)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change(&a.pipelines[id].definition)
}

func (a *fakePipelinesAPI) delete(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pipelines, id)
}

func refusePipeline(w http.ResponseWriter, format string, args ...any) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": fmt.Sprintf(format, args...)})
}

// normalizeCertificates stores the certificates alone, written anew, as the
// engine does. Like the engine's PEM parser, and unlike Go's, it reads a block
// whatever text surrounds it and whatever whitespace its lines hold.
func normalizeCertificates(text string) (string, bool) {
	var out bytes.Buffer
	for {
		_, block, found := strings.Cut(text, "-----BEGIN ")
		if !found {
			break
		}
		label, rest, _ := strings.Cut(block, "-----")
		data, rest, _ := strings.Cut(rest, "-----END "+label+"-----")
		text = rest
		if label != "CERTIFICATE" && label != "X509 CERTIFICATE" {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(data), ""))
		if err != nil {
			return "", false
		}
		if _, err := x509.ParseCertificate(der); err != nil {
			return "", false
		}
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	return out.String(), out.Len() > 0
}

func hasNull(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case map[string]any:
		return slices.ContainsFunc(slices.Collect(maps.Values(v)), hasNull)
	case []any:
		return slices.ContainsFunc(v, hasNull)
	}
	return false
}

var fakePipelineNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// parseBody validates a create/replace body like the engine's PipelineBody:
// undeclared fields, an empty list or map where the API wants none, and
// secrets the Vault settings cannot read are a 422.
func (a *fakePipelinesAPI) parseBody(w http.ResponseWriter, r *http.Request) (fakePipelineBody, bool) {
	var body fakePipelineBody
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		refusePipeline(w, "%s", err)
		return body, false
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		refusePipeline(w, "%s", err)
		return body, false
	}
	// An optional field is absent or set, never null: only `vault` is
	// nullable.
	if fields["vault"] == nil {
		delete(fields, "vault")
	}
	if hasNull(fields) {
		refusePipeline(w, "Input should not be null")
		return body, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		refusePipeline(w, "%s", err)
		return body, false
	}
	if !fakePipelineNamePattern.MatchString(body.Name) {
		refusePipeline(w, "invalid name %q", body.Name)
		return body, false
	}
	if body.Triggers == nil {
		body.Triggers = &[]fakePipelineTrigger{{Context: "pull_request"}, {Context: "merge_queue"}}
	}
	seen := map[string]bool{}
	for _, trigger := range *body.Triggers {
		switch {
		case trigger.Context == "push":
			refusePipeline(w, "`push` triggers are not supported yet")
			return body, false
		case trigger.Context != "pull_request" && trigger.Context != "merge_queue":
			refusePipeline(w, "unknown context %q", trigger.Context)
			return body, false
		case seen[trigger.Context]:
			refusePipeline(w, "Only one `%s` trigger is allowed", trigger.Context)
			return body, false
		case trigger.Context == "pull_request" && trigger.Queues != nil,
			trigger.Context == "merge_queue" && trigger.Branches != nil:
			refusePipeline(w, "Extra inputs are not permitted")
			return body, false
		case trigger.Branches != nil && len(trigger.Branches) == 0,
			trigger.Queues != nil && len(trigger.Queues) == 0:
			refusePipeline(w, "List should have at least 1 item")
			return body, false
		}
		seen[trigger.Context] = true
	}
	job := body.InitialJob
	switch {
	case job == nil || job.Image == "":
		refusePipeline(w, "initial_job.image is required")
		return body, false
	case job.Args != nil && len(job.Args) == 0:
		refusePipeline(w, "initial_job.args should have at least 1 item")
		return body, false
	case job.Secrets != nil && len(job.Secrets) == 0:
		refusePipeline(w, "initial_job.secrets should have at least 1 item")
		return body, false
	case len(job.Secrets) > 0 && body.Vault == nil:
		refusePipeline(w, "The initial job declares secrets: `vault` is required")
		return body, false
	}
	for name, secret := range job.Secrets {
		if secret.Role == nil && body.Vault.DefaultRole == nil {
			refusePipeline(w, "Secrets %s name no role, and `vault` has no `default_role`", name)
			return body, false
		}
	}
	if vault := body.Vault; vault != nil {
		if !strings.HasPrefix(vault.Address, "https://") {
			refusePipeline(w, "The Vault address must be an https:// URL")
			return body, false
		}
		if vault.CACertificate != nil {
			normalized, ok := normalizeCertificates(*vault.CACertificate)
			if !ok {
				refusePipeline(w, "The CA certificate must be PEM certificates")
				return body, false
			}
			vault.CACertificate = &normalized
		}
	}
	return body, true
}

// refuseTakenName answers 409 when another pipeline of the repository has
// the name, whatever the case.
func (a *fakePipelinesAPI) refuseTakenName(w http.ResponseWriter, repository, name string, except int64) bool {
	for id, p := range a.pipelines {
		if id != except && p.repository == repository && strings.EqualFold(p.definition.Name, name) {
			writeJSON(w, http.StatusConflict, map[string]string{"detail": "The repository already has a pipeline named `" + name + "`", "error_code": "PIPELINE_NAME_TAKEN"})
			return true
		}
	}
	return false
}

func (a *fakePipelinesAPI) pipelineJSON(id int64, p *fakePipeline) map[string]any {
	// Through JSON, so that an absent field stays absent.
	raw, _ := json.Marshal(p.definition)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	out["id"] = id
	out["created_at"] = "2026-10-21T00:00:00Z"
	out["updated_at"] = "2026-10-21T00:00:00Z"
	return out
}

func (a *fakePipelinesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Unauthorized"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" || !strings.EqualFold(parts[1], a.owner) || !slices.Contains(a.repos, parts[2]) || parts[3] != "pipelines" {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
		return
	}
	repository := parts[2]

	if len(parts) == 4 && r.Method == http.MethodPost {
		body, ok := a.parseBody(w, r)
		if !ok {
			return
		}
		if a.refuseTakenName(w, repository, body.Name, 0) {
			return
		}
		a.nextID++
		p := &fakePipeline{repository: repository, definition: body}
		a.pipelines[a.nextID] = p
		writeJSON(w, http.StatusCreated, a.pipelineJSON(a.nextID, p))
		return
	}
	if len(parts) == 5 {
		id, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			refusePipeline(w, "pipeline_id should be a valid integer")
			return
		}
		p, ok := a.pipelines[id]
		if !ok || p.repository != repository {
			writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Pipeline not found", "error_code": "PIPELINE_NOT_FOUND"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, a.pipelineJSON(id, p))
			return
		case http.MethodPut:
			body, ok := a.parseBody(w, r)
			if !ok {
				return
			}
			if a.refuseTakenName(w, repository, body.Name, id) {
				return
			}
			p.definition = body
			writeJSON(w, http.StatusOK, a.pipelineJSON(id, p))
			return
		case http.MethodDelete:
			delete(a.pipelines, id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
}

// checkFakePipeline checks the definition the API stores for a pipeline,
// written as the JSON the API answers with, triggers sorted by context.
func checkFakePipeline(api *fakePipelinesAPI, id int64, repository, want string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		p, ok := api.pipelines[id]
		if !ok {
			return fmt.Errorf("no pipeline %d", id)
		}
		if p.repository != repository {
			return fmt.Errorf("pipeline %d is in %s, want %s", id, p.repository, repository)
		}
		// Triggers, branches and queues are sets in Terraform: their order
		// is not the configuration's.
		definition := p.definition
		triggers := slices.Clone(*definition.Triggers)
		for i := range triggers {
			triggers[i].Branches = slices.Sorted(slices.Values(triggers[i].Branches))
			triggers[i].Queues = slices.Sorted(slices.Values(triggers[i].Queues))
		}
		slices.SortFunc(triggers, func(a, b fakePipelineTrigger) int { return strings.Compare(a.Context, b.Context) })
		definition.Triggers = &triggers
		raw, _ := json.Marshal(definition)
		var got, expected any
		_ = json.Unmarshal(raw, &got)
		if err := json.Unmarshal([]byte(want), &expected); err != nil {
			return fmt.Errorf("decode want: %w", err)
		}
		if !reflect.DeepEqual(got, expected) {
			return fmt.Errorf("pipeline %d: got %s, want %s", id, raw, want)
		}
		return nil
	}
}

func checkPipelineCount(api *fakePipelinesAPI, want int) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if got := api.count(); got != want {
			return fmt.Errorf("got %d pipelines, want %d", got, want)
		}
		return nil
	}
}

// testCertificate returns a self-signed certificate in PEM, as the engine
// writes it.
func testCertificate(t *testing.T) string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "vault.acme.test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestPipelineResource(t *testing.T) {
	api, srv := newFakePipelinesAPI(t)
	certificate, second := testCertificate(t), testCertificate(t)
	full := func(caCertificate string) string {
		return providerConfig(srv) + fmt.Sprintf(`
resource "mergify_pipeline" "ci" {
  owner      = "acme"
  repository = "engine"
  name       = "CI"
  triggers = [
    { context = "pull_request", branches = ["main", "release/*"] },
    { context = "merge_queue", queues = ["default"] },
  ]
  initial_job = {
    image   = "ghcr.io/acme/ci:1"
    args    = ["generate", "--all"]
    runs_on = "ubuntu-24.04"
    secrets = {
      REGISTRY_TOKEN = { path = "secret/data/ci/registry", field = "token", role = "registry", env = true }
      NPM_TOKEN      = { path = "secret/data/ci/npm", field = "token", env = false }
    }
  }
  vault = {
    address        = "https://vault.acme.test:8200"
    audience       = "mergify"
    auth_mount     = "mergify-jwt"
    namespace      = "ci"
    ca_certificate = %q
    default_role   = "ci"
  }
}
`, caCertificate)
	}
	fullJSON := func(caCertificate string) string {
		return fmt.Sprintf(`{
  "name": "CI",
  "triggers": [
    {"context": "merge_queue", "queues": ["default"]},
    {"context": "pull_request", "branches": ["main", "release/*"]}
  ],
  "initial_job": {
    "image": "ghcr.io/acme/ci:1",
    "args": ["generate", "--all"],
    "runs_on": "ubuntu-24.04",
    "secrets": {
      "REGISTRY_TOKEN": {"path": "secret/data/ci/registry", "field": "token", "role": "registry", "env": true},
      "NPM_TOKEN": {"path": "secret/data/ci/npm", "field": "token", "env": false}
    }
  },
  "vault": {
    "address": "https://vault.acme.test:8200",
    "audience": "mergify",
    "auth_mount": "mergify-jwt",
    "namespace": "ci",
    "ca_certificate": %q,
    "default_role": "ci"
  }
}`, caCertificate)
	}
	// The same certificate with Windows line endings and its key pasted
	// along, which the API drops.
	pastedWithKey := strings.ReplaceAll(certificate, "\n", "\r\n") +
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key")}))
	// Spellings the engine reads and Go's PEM decoder does not: text before
	// the first boundary, indented lines, a bundle joined without a newline.
	indent := func(text string) string {
		return strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n    ")
	}
	loose := "subject=CN=vault.acme.test " + indent(certificate) + indent(second) + "\n"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkPipelineCount(api, 0),
		Steps: []resource.TestStep{
			{
				// Triggers left out are the API's default ones.
				Config: providerConfig(srv) + `
resource "mergify_pipeline" "ci" {
  owner       = "acme"
  repository  = "engine"
  name        = "ci"
  initial_job = { image = "ghcr.io/acme/ci:1" }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "id", "1"),
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "triggers.#", "2"),
					resource.TestCheckNoResourceAttr("mergify_pipeline.ci", "vault"),
					checkFakePipeline(api, 1, "engine", `{
  "name": "ci",
  "triggers": [{"context": "merge_queue"}, {"context": "pull_request"}],
  "initial_job": {"image": "ghcr.io/acme/ci:1"},
  "vault": null
}`),
				),
			},
			{
				// Every field, and a rename, updated in place.
				Config: full(certificate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "id", "1"),
					checkFakePipeline(api, 1, "engine", fullJSON(certificate)),
				),
			},
			{
				ResourceName:      "mergify_pipeline.ci",
				ImportState:       true,
				ImportStateId:     "acme/engine/1",
				ImportStateVerify: true,
			},
			{
				// Another spelling of the same certificate is stored as the
				// API writes it and keeps the plan empty.
				Config: full(pastedWithKey),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "vault.ca_certificate", pastedWithKey),
					checkFakePipeline(api, 1, "engine", fullJSON(certificate)),
				),
			},
			{
				Config: full(loose),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "vault.ca_certificate", loose),
					checkFakePipeline(api, 1, "engine", fullJSON(certificate+second)),
				),
			},
			{
				// Changed outside Terraform: the plan changes it back.
				PreConfig: func() {
					api.update(1, func(p *fakePipelineBody) { p.InitialJob.Image = "ghcr.io/acme/ci:0" })
				},
				Config:             full(loose),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Empty triggers: nothing runs the pipeline. Without Vault
				// settings, the initial job reads no secret. The owner spelled
				// in another case is the same organization: no replacement.
				Config: providerConfig(srv) + `
resource "mergify_pipeline" "ci" {
  owner       = "Acme"
  repository  = "engine"
  name        = "ci"
  triggers    = []
  initial_job = { image = "ghcr.io/acme/ci:2" }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "id", "1"),
					checkFakePipeline(api, 1, "engine", `{
  "name": "ci",
  "triggers": [],
  "initial_job": {"image": "ghcr.io/acme/ci:2"},
  "vault": null
}`),
				),
			},
			{
				// Deleted outside Terraform: the next apply recreates it.
				PreConfig: func() { api.delete(1) },
				Config: providerConfig(srv) + `
resource "mergify_pipeline" "ci" {
  owner       = "acme"
  repository  = "engine"
  name        = "ci"
  initial_job = { image = "ghcr.io/acme/ci:2" }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "id", "2"),
					checkPipelineCount(api, 1),
				),
			},
			{
				// Moving to another repository replaces the pipeline.
				Config: providerConfig(srv) + `
resource "mergify_pipeline" "ci" {
  owner       = "acme"
  repository  = "docs"
  name        = "ci"
  initial_job = { image = "ghcr.io/acme/ci:2" }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_pipeline.ci", "id", "3"),
					checkFakePipeline(api, 3, "docs", `{
  "name": "ci",
  "triggers": [{"context": "merge_queue"}, {"context": "pull_request"}],
  "initial_job": {"image": "ghcr.io/acme/ci:2"},
  "vault": null
}`),
					checkPipelineCount(api, 1),
				),
			},
		},
	})
}

func TestPipelineResourceRefused(t *testing.T) {
	_, srv := newFakePipelinesAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				// An empty list is not an absent one: the API refuses it.
				Config: providerConfig(srv) + `
resource "mergify_pipeline" "ci" {
  owner       = "acme"
  repository  = "engine"
  name        = "ci"
  initial_job = { image = "ghcr.io/acme/ci:1", args = [] }
}
`,
				ExpectError: regexp.MustCompile(`initial_job.args should\s+have at least 1 item`),
			},
			{
				Config: providerConfig(srv) + `
resource "mergify_pipeline" "ci" {
  owner       = "acme"
  repository  = "engine"
  name        = "ci"
  initial_job = { image = "ghcr.io/acme/ci:1" }
}
resource "mergify_pipeline" "other" {
  owner       = "acme"
  repository  = "engine"
  name        = "CI"
  initial_job = { image = "ghcr.io/acme/ci:1" }
  depends_on  = [mergify_pipeline.ci]
}
`,
				ExpectError: regexp.MustCompile(`PIPELINE_NAME_TAKEN`),
			},
			{
				ResourceName:  "mergify_pipeline.ci",
				ImportState:   true,
				ImportStateId: "acme/engine/ci",
				ExpectError:   regexp.MustCompile(`The pipeline ID is a number`),
			},
			{
				// A 404 that is not the pipeline's is an error, not a pipeline
				// gone: the token may not see the repository.
				ResourceName:  "mergify_pipeline.ci",
				ImportState:   true,
				ImportStateId: "acme/secret/1",
				ExpectError:   regexp.MustCompile(`Mergify API error reading pipeline`),
			},
		},
	})
}
