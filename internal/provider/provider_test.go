package provider

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const slackChannelLookup = `
data "mergify_slack_channel" "alerts" {
  owner = "acme"
  name  = "alerts"
}
`

func providerConfigWithoutToken(srv *httptest.Server) string {
	return fmt.Sprintf(`
provider "mergify" {
  endpoint = %q
}
`, srv.URL)
}

func TestProviderIgnoresGitHubToken(t *testing.T) {
	_, srv := newFakeSlackAPI(t)
	t.Setenv("MERGIFY_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "ghp_not_a_mergify_credential")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      providerConfigWithoutToken(srv) + slackChannelLookup,
				ExpectError: regexp.MustCompile(`(?s)Missing Mergify API token.*GITHUB_TOKEN environment variable is no\s+longer used`),
			},
		},
	})
}

func TestProviderReadsMergifyTokenEnv(t *testing.T) {
	_, srv := newFakeSlackAPI(t)
	t.Setenv("MERGIFY_TOKEN", "test-token")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories(),
		Steps: []resource.TestStep{
			{Config: providerConfigWithoutToken(srv) + slackChannelLookup},
		},
	})
}
