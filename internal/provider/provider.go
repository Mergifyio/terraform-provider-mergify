package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*MergifyProvider)(nil)

type MergifyProvider struct {
	version string
}

type MergifyProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &MergifyProvider{version: version}
	}
}

func (p *MergifyProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "mergify"
	resp.Version = p.version
}

func (p *MergifyProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Mergify resources via the Mergify API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "Mergify API base URL. Defaults to `https://api.mergify.com/v1`. May also be set via the `MERGIFY_ENDPOINT` environment variable.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Mergify API bearer token. May also be set via the `MERGIFY_TOKEN` environment variable. As a fallback the `GITHUB_TOKEN` environment variable is used (the Mergify API also accepts GitHub personal access tokens).",
			},
		},
	}
}

func (p *MergifyProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data MergifyProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := os.Getenv("MERGIFY_ENDPOINT")
	if !data.Endpoint.IsNull() {
		endpoint = data.Endpoint.ValueString()
	}
	if endpoint == "" {
		endpoint = "https://api.mergify.com/v1"
	}

	token := os.Getenv("MERGIFY_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if !data.Token.IsNull() {
		token = data.Token.ValueString()
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing Mergify API token",
			"Set the `token` provider attribute, or the MERGIFY_TOKEN or GITHUB_TOKEN environment variable.",
		)
		return
	}

	client := NewClient(endpoint, token, p.version)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *MergifyProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewRepositoryProductsResource,
		NewOrganizationDefaultProductsResource,
	}
}

func (p *MergifyProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
