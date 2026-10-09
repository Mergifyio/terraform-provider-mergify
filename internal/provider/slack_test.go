package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Body fields each product accepts, as declared by the engine's
// `web/api/integrations/slack.py` payload models.
var fakeSlackProductFields = map[string][]string{
	"merge_queue_notification":     {"repositories", "event_types"},
	"ci_insights_notification":     {"repositories", "branches", "pipeline_names", "job_names", "conclusions"},
	"test_quarantine_notification": {"repositories", "event_types"},
}

var fakeSlackResponseKeys = map[string]string{
	"merge_queue_notification":     "merge_queue_configs",
	"ci_insights_notification":     "ci_insights_configs",
	"test_quarantine_notification": "test_quarantine_configs",
}

type fakeSlackChannel struct {
	id      string
	name    string
	team    string
	// When Mergify can no longer post to the channel, e.g. "webhook_gone".
	disabledReason string
	configs map[string]map[string]map[string][]string // product -> id -> field -> values
}

// fakeSlackAPI mimics the Mergify Slack integration API for one organization.
type fakeSlackAPI struct {
	mu       sync.Mutex
	owner    string
	repos    []string // GitHub casing
	channels []*fakeSlackChannel
	nextID   int
}

func newFakeSlackAPI(t *testing.T) (*fakeSlackAPI, *httptest.Server) {
	api := &fakeSlackAPI{
		owner: "acme",
		repos: []string{"Engine", "docs"},
		channels: []*fakeSlackChannel{
			{id: "chan-alerts", name: "#alerts", team: "Acme"},
			{id: "chan-ci", name: "#ci", team: "Acme"},
			{id: "chan-dup-1", name: "#dup", team: "Acme"},
			{id: "chan-dup-2", name: "#dup", team: "Partner"},
			{id: "chan-alerts-old", name: "#alerts", team: "Acme", disabledReason: "webhook_gone"},
		},
	}
	for _, ch := range api.channels {
		ch.configs = map[string]map[string]map[string][]string{}
		for product := range fakeSlackProductFields {
			ch.configs[product] = map[string]map[string][]string{}
		}
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, srv
}

func (a *fakeSlackAPI) channel(id string) *fakeSlackChannel {
	for _, ch := range a.channels {
		if strings.EqualFold(ch.id, id) {
			return ch
		}
	}
	return nil
}

func (a *fakeSlackAPI) configCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, ch := range a.channels {
		for _, configs := range ch.configs {
			n += len(configs)
		}
	}
	return n
}

// onlyConfig returns the single configuration of a product on a channel, or
// nil when there is none.
func (a *fakeSlackAPI) onlyConfig(channelID, product string) map[string][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, fields := range a.channel(channelID).configs[product] {
		return fields
	}
	return nil
}

// addDuplicate appends a copy of the first value of a field, as the API
// accepts duplicates.
func (a *fakeSlackAPI) addDuplicate(channelID, product, field string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, fields := range a.channel(channelID).configs[product] {
		fields[field] = append(fields[field], fields[field][0])
	}
}

func (a *fakeSlackAPI) deleteConfig(channelID, product, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.channel(channelID).configs[product], id)
}

func (a *fakeSlackAPI) configJSON(id string, fields map[string][]string) map[string]any {
	out := map[string]any{"id": id}
	for field, values := range fields {
		if field != "repositories" {
			out[field] = values
			continue
		}
		repos := []map[string]any{}
		for _, name := range values {
			repos = append(repos, map[string]any{"id": 1, "name": name, "full_name": a.owner + "/" + name, "owner": map[string]any{"id": 2, "login": a.owner, "type": "Organization"}})
		}
		out[field] = repos
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// parseBody validates a create/update body like the API does: undeclared
// fields and unknown repositories are a 422, a field left out is empty.
func (a *fakeSlackAPI) parseBody(w http.ResponseWriter, r *http.Request, product string) (map[string][]string, bool) {
	var body map[string][]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return nil, false
	}
	fields := map[string][]string{}
	for _, f := range fakeSlackProductFields[product] {
		fields[f] = []string{}
	}
	for field, values := range body {
		if _, ok := fields[field]; !ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "Extra inputs are not permitted: " + field})
			return nil, false
		}
		fields[field] = values
	}
	for i, name := range fields["repositories"] {
		found := false
		for _, repo := range a.repos {
			if strings.EqualFold(repo, name) {
				fields["repositories"][i] = repo
				found = true
			}
		}
		if !found {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "Repository not found: " + name})
			return nil, false
		}
	}
	return fields, true
}

func (a *fakeSlackAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Unauthorized"})
		return
	}
	prefix := "/integrations/" + a.owner + "/configuration/slack"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")
	if parts[0] == "" {
		parts = nil
	}

	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		channels := []map[string]any{}
		for _, ch := range a.channels {
			out := map[string]any{"id": ch.id, "slack_channel": ch.name, "slack_team_name": ch.team, "disabled_at": nil, "disabled_reason": nil}
			if ch.disabledReason != "" {
				out["disabled_at"] = "2026-10-01T00:00:00Z"
				out["disabled_reason"] = ch.disabledReason
			}
			for product, key := range fakeSlackResponseKeys {
				configs := []map[string]any{}
				for id, fields := range ch.configs[product] {
					configs = append(configs, a.configJSON(id, fields))
				}
				out[key] = configs
			}
			channels = append(channels, out)
		}
		writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
		return
	case len(parts) >= 2:
		ch := a.channel(parts[0])
		product := parts[1]
		if ch == nil || fakeSlackProductFields[product] == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			fields, ok := a.parseBody(w, r, product)
			if !ok {
				return
			}
			a.nextID++
			id := fmt.Sprintf("config-%d", a.nextID)
			ch.configs[product][id] = fields
			writeJSON(w, http.StatusCreated, a.configJSON(id, fields))
			return
		}
		if len(parts) == 3 {
			id := parts[2]
			if _, ok := ch.configs[product][id]; !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
				return
			}
			switch r.Method {
			case http.MethodPut:
				fields, ok := a.parseBody(w, r, product)
				if !ok {
					return
				}
				ch.configs[product][id] = fields
				w.WriteHeader(http.StatusNoContent)
				return
			case http.MethodDelete:
				delete(ch.configs[product], id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
}

func testProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"mergify": providerserver.NewProtocol6WithError(New("test")()),
	}
}

func providerConfig(srv *httptest.Server) string {
	return fmt.Sprintf(`
provider "mergify" {
  endpoint = %q
  token    = "test-token"
}
`, srv.URL)
}

func sorted(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func checkFakeConfig(api *fakeSlackAPI, channelID, product string, want map[string][]string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		got := api.onlyConfig(channelID, product)
		if got == nil {
			return fmt.Errorf("no %s configuration on %s", product, channelID)
		}
		for field, values := range want {
			if fmt.Sprint(sorted(got[field])) != fmt.Sprint(sorted(values)) {
				return fmt.Errorf("%s: got %v, want %v", field, got[field], values)
			}
		}
		return nil
	}
}

func checkConfigCount(api *fakeSlackAPI, want int) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if got := api.configCount(); got != want {
			return fmt.Errorf("got %d notification configurations, want %d", got, want)
		}
		return nil
	}
}

func TestSlackChannelDataSource(t *testing.T) {
	_, srv := newFakeSlackAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv) + `
data "mergify_slack_channel" "with_hash" {
  owner = "acme"
  name  = "#alerts"
}
data "mergify_slack_channel" "without_hash" {
  owner = "acme"
  name  = "alerts"
}
data "mergify_slack_channel" "by_workspace" {
  owner     = "acme"
  name      = "dup"
  workspace = "Partner"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.mergify_slack_channel.with_hash", "id", "chan-alerts"),
					resource.TestCheckResourceAttr("data.mergify_slack_channel.without_hash", "id", "chan-alerts"),
					resource.TestCheckNoResourceAttr("data.mergify_slack_channel.with_hash", "disabled_at"),
					resource.TestCheckResourceAttr("data.mergify_slack_channel.by_workspace", "id", "chan-dup-2"),
				),
			},
			{
				Config: providerConfig(srv) + `
data "mergify_slack_channel" "ambiguous" {
  owner = "acme"
  name  = "dup"
}
`,
				ExpectError: regexp.MustCompile(`Several Slack channels match`),
			},
			{
				Config: providerConfig(srv) + `
data "mergify_slack_channel" "missing" {
  owner = "acme"
  name  = "nope"
}
`,
				ExpectError: regexp.MustCompile(`Slack channel not found`),
			},
		},
	})
}

func TestSlackMergeQueueNotificationResource(t *testing.T) {
	api, srv := newFakeSlackAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkConfigCount(api, 0),
		Steps: []resource.TestStep{
			{
				// "engine" is spelled "Engine" on GitHub: the state keeps the
				// configured casing so the plan stays empty.
				Config: providerConfig(srv) + `
data "mergify_slack_channel" "alerts" {
  owner = "acme"
  name  = "alerts"
}
resource "mergify_slack_merge_queue_notification" "mq" {
  owner        = "acme"
  channel_id   = data.mergify_slack_channel.alerts.id
  repositories = ["engine"]
  event_types  = ["action.queue.merged", "queue.pause.create"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_slack_merge_queue_notification.mq", "id", "config-1"),
					resource.TestCheckTypeSetElemAttr("mergify_slack_merge_queue_notification.mq", "repositories.*", "engine"),
					checkFakeConfig(api, "chan-alerts", "merge_queue_notification", map[string][]string{
						"repositories": {"Engine"},
						"event_types":  {"action.queue.merged", "queue.pause.create"},
					}),
				),
			},
			{
				// Omitting the filters clears them: every repository, every event.
				Config: providerConfig(srv) + `
resource "mergify_slack_merge_queue_notification" "mq" {
  owner      = "acme"
  channel_id = "chan-alerts"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_slack_merge_queue_notification.mq", "id", "config-1"),
					resource.TestCheckResourceAttr("mergify_slack_merge_queue_notification.mq", "repositories.#", "0"),
					resource.TestCheckResourceAttr("mergify_slack_merge_queue_notification.mq", "event_types.#", "0"),
					checkFakeConfig(api, "chan-alerts", "merge_queue_notification", map[string][]string{
						"repositories": {},
						"event_types":  {},
					}),
				),
			},
			{
				ResourceName:      "mergify_slack_merge_queue_notification.mq",
				ImportState:       true,
				ImportStateId:     "acme/chan-alerts/config-1",
				ImportStateVerify: true,
			},
			{
				// Deleted outside Terraform: the next apply recreates it.
				PreConfig: func() { api.deleteConfig("chan-alerts", "merge_queue_notification", "config-1") },
				Config: providerConfig(srv) + `
resource "mergify_slack_merge_queue_notification" "mq" {
  owner      = "acme"
  channel_id = "chan-alerts"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_slack_merge_queue_notification.mq", "id", "config-2"),
					checkConfigCount(api, 1),
				),
			},
			{
				// Moving to another channel replaces the configuration. The ID
				// is spelled in another case than the API answers with.
				Config: providerConfig(srv) + `
resource "mergify_slack_merge_queue_notification" "mq" {
  owner      = "acme"
  channel_id = "CHAN-CI"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mergify_slack_merge_queue_notification.mq", "channel_id", "CHAN-CI"),
					checkConfigCount(api, 1),
				),
			},
		},
	})
}

func TestSlackCIInsightsNotificationResource(t *testing.T) {
	api, srv := newFakeSlackAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkConfigCount(api, 0),
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv) + `
resource "mergify_slack_ci_insights_notification" "ci" {
  owner          = "acme"
  channel_id     = "chan-ci"
  repositories   = ["Engine", "docs"]
  branches       = ["main"]
  pipeline_names = ["ci"]
  job_names      = ["test"]
  conclusions    = ["failure"]
}
`,
				Check: checkFakeConfig(api, "chan-ci", "ci_insights_notification", map[string][]string{
					"repositories":   {"Engine", "docs"},
					"branches":       {"main"},
					"pipeline_names": {"ci"},
					"job_names":      {"test"},
					"conclusions":    {"failure"},
				}),
			},
			{
				Config: providerConfig(srv) + `
resource "mergify_slack_ci_insights_notification" "ci" {
  owner       = "acme"
  channel_id  = "chan-ci"
  branches    = ["main", "release"]
  conclusions = ["failure", "cancelled"]
}
`,
				Check: checkFakeConfig(api, "chan-ci", "ci_insights_notification", map[string][]string{
					"repositories":   {},
					"branches":       {"main", "release"},
					"pipeline_names": {},
					"job_names":      {},
					"conclusions":    {"failure", "cancelled"},
				}),
			},
			{
				// A duplicate stored through the API is not a diff.
				PreConfig: func() { api.addDuplicate("chan-ci", "ci_insights_notification", "branches") },
				Config: providerConfig(srv) + `
resource "mergify_slack_ci_insights_notification" "ci" {
  owner       = "acme"
  channel_id  = "chan-ci"
  branches    = ["main", "release"]
  conclusions = ["failure", "cancelled"]
}
`,
				PlanOnly: true,
			},
			{
				ResourceName:      "mergify_slack_ci_insights_notification.ci",
				ImportState:       true,
				ImportStateId:     "acme/chan-ci/config-1",
				ImportStateVerify: true,
			},
		},
	})
}

func TestSlackTestQuarantineNotificationResource(t *testing.T) {
	api, srv := newFakeSlackAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		CheckDestroy:             checkConfigCount(api, 0),
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv) + `
resource "mergify_slack_test_quarantine_notification" "tq" {
  owner        = "acme"
  channel_id   = "chan-ci"
  repositories = ["docs"]
  event_types  = ["test.quarantined"]
}
`,
				Check: checkFakeConfig(api, "chan-ci", "test_quarantine_notification", map[string][]string{
					"repositories": {"docs"},
					"event_types":  {"test.quarantined"},
				}),
			},
			{
				Config: providerConfig(srv) + `
resource "mergify_slack_test_quarantine_notification" "tq" {
  owner        = "acme"
  channel_id   = "chan-ci"
  repositories = ["unknown"]
}
`,
				ExpectError: regexp.MustCompile(`Repository not found: unknown`),
			},
			{
				ResourceName:  "mergify_slack_test_quarantine_notification.tq",
				ImportState:   true,
				ImportStateId: "acme/chan-ci",
				ExpectError:   regexp.MustCompile(`Invalid import ID`),
			},
		},
	})
}
