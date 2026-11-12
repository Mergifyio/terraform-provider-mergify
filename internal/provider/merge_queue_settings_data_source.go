package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*EffectiveMergeQueueSettingsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*EffectiveMergeQueueSettingsDataSource)(nil)
)

type EffectiveMergeQueueSettingsDataSource struct {
	client *Client
}

type EffectiveMergeQueueSettingsDataSourceModel struct {
	ID                     types.String `tfsdk:"id"`
	Owner                  types.String `tfsdk:"owner"`
	Repository             types.String `tfsdk:"repository"`
	Branch                 types.String `tfsdk:"branch"`
	BatchPullRequest       types.Bool   `tfsdk:"batch_pull_request"`
	BatchPullRequestSource types.Object `tfsdk:"batch_pull_request_source"`
}

var mergeQueueSettingSourceType = map[string]attr.Type{
	"level":  types.StringType,
	"branch": types.StringType,
}

func NewEffectiveMergeQueueSettingsDataSource() datasource.DataSource {
	return &EffectiveMergeQueueSettingsDataSource{}
}

func (d *EffectiveMergeQueueSettingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_effective_merge_queue_settings"
}

func (d *EffectiveMergeQueueSettingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Read the merge queue settings a base branch uses once the branch, repository and organization levels are resolved, each with the level it comes from.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Identifier in the form `<owner>/<repository>:<branch>`.",
			},
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "GitHub organization or user that owns the repository.",
			},
			"repository": schema.StringAttribute{
				Required:    true,
				Description: "GitHub repository name.",
			},
			"branch": schema.StringAttribute{
				Required:    true,
				Description: "Name of the base branch.",
			},
			"batch_pull_request": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the merge queue of the base branch opens a draft pull request to check a batch.",
			},
			"batch_pull_request_source": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "Where `batch_pull_request` comes from.",
				Attributes: map[string]schema.Attribute{
					"level": schema.StringAttribute{
						Computed:    true,
						Description: "The level the value comes from: `branch`, `repository`, `organization` or `default`.",
					},
					"branch": schema.StringAttribute{
						Computed:    true,
						Description: "The base branch name or pattern that supplied the value, for the `branch` level; null otherwise.",
					},
				},
			},
		},
	}
}

func (d *EffectiveMergeQueueSettingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *Client, got %T. This is a provider bug; please report it.", req.ProviderData),
		)
		return
	}
	d.client = client
}

func (d *EffectiveMergeQueueSettingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data EffectiveMergeQueueSettingsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repository, branch := data.Owner.ValueString(), data.Repository.ValueString(), data.Branch.ValueString()
	effective, err := d.client.GetEffectiveMergeQueueSettings(ctx, owner, repository, branch)
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error reading effective merge queue settings", err.Error())
		return
	}

	source := effective.Sources.BatchPullRequest
	sourceValue, diags := types.ObjectValue(mergeQueueSettingSourceType, map[string]attr.Value{
		"level":  types.StringValue(source.Level),
		"branch": types.StringPointerValue(source.Branch),
	})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(owner + "/" + repository + ":" + branch)
	data.BatchPullRequest = types.BoolValue(effective.Settings.BatchPullRequest)
	data.BatchPullRequestSource = sourceValue
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
