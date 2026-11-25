package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*MergeQueueBranchSettingsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*MergeQueueBranchSettingsDataSource)(nil)
)

type MergeQueueBranchSettingsDataSource struct {
	client *Client
}

type MergeQueueBranchSettingsDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Owner      types.String `tfsdk:"owner"`
	Repository types.String `tfsdk:"repository"`
	Branch     types.String `tfsdk:"branch"`
	Settings   types.Object `tfsdk:"settings"`
	Decisions  types.Object `tfsdk:"decisions"`
	Policies   types.List   `tfsdk:"policies"`
}

var mergeQueueSettingDecisionType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"rule":       types.StringType,
	"policy_ids": types.ListType{ElemType: types.StringType},
}}

var mergeQueueSettingDecisionsType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"batch_pull_request": mergeQueueSettingDecisionType,
}}

var mergeQueueAppliedPolicyType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":         types.StringType,
	"name":       types.StringType,
	"repository": types.StringType,
	"target": types.ObjectType{AttrTypes: map[string]attr.Type{
		"repositories": mergeQueuePolicyPatternsType,
		"branches":     mergeQueuePolicyPatternsType,
	}},
	"settings": mergeQueuePolicySettingsType,
}}

func NewMergeQueueBranchSettingsDataSource() datasource.DataSource {
	return &MergeQueueBranchSettingsDataSource{}
}

func (d *MergeQueueBranchSettingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_merge_queue_branch_settings"
}

func computedPatternsAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Computed:    true,
		Description: description,
		Attributes: map[string]schema.Attribute{
			"include_patterns": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "The ones targeted.",
			},
			"exclude_patterns": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "The ones never targeted, even when included.",
			},
		},
	}
}

func (d *MergeQueueBranchSettingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Read the merge queue settings a base branch uses, how each value was settled, and every merge queue policy that applies to the branch. " + mergeQueuePolicyRule,
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
				Description: "Name of the base branch. Any branch name works, whether or not the branch exists.",
			},
			"settings": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "What the merge queue of the base branch uses.",
				Attributes: map[string]schema.Attribute{
					"batch_pull_request": schema.BoolAttribute{
						Computed:    true,
						Description: "Whether the merge queue of the base branch opens a draft pull request to check a batch.",
					},
				},
			},
			"decisions": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "How each value of `settings` was settled.",
				Attributes: map[string]schema.Attribute{
					"batch_pull_request": schema.SingleNestedAttribute{
						Computed:    true,
						Description: "How `batch_pull_request` was settled.",
						Attributes: map[string]schema.Attribute{
							"rule": schema.StringAttribute{
								Computed:    true,
								Description: "`default` when no policy that applies sets the value, `policies_agree` when they all set the same value, `policies_disagree` when they do not and the safest value won.",
							},
							"policy_ids": schema.ListAttribute{
								Computed:    true,
								ElementType: types.StringType,
								Description: "The ids of the policies that set the value the branch gets; empty for `default`.",
							},
						},
					},
				},
			},
			"policies": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every active merge queue policy that applies to the base branch.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "The id of the policy.",
						},
						"name": schema.StringAttribute{
							Computed:    true,
							Description: "The policy's name.",
						},
						"repository": schema.StringAttribute{
							Computed:    true,
							Description: "The repository holding the policy; null for an organization's policy.",
						},
						"target": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "What the policy applies to.",
							Attributes: map[string]schema.Attribute{
								"repositories": computedPatternsAttribute("The repositories the policy applies to; null for a repository's policy."),
								"branches":     computedPatternsAttribute("The base branches the policy applies to."),
							},
						},
						"settings": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "The merge queue settings the policy sets; a setting it leaves alone is null.",
							Attributes: map[string]schema.Attribute{
								"batch_pull_request": schema.BoolAttribute{
									Computed:    true,
									Description: "Whether the merge queue opens a draft pull request to check a batch.",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *MergeQueueBranchSettingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func appliedPolicyValue(ctx context.Context, policy MergeQueuePolicy, diags *diag.Diagnostics) attr.Value {
	targetType := mergeQueueAppliedPolicyType.AttrTypes["target"].(types.ObjectType)
	repositories := types.ObjectNull(mergeQueuePolicyPatternsType.AttrTypes)
	if policy.Target.Repositories != nil {
		repositories = patternsValue(ctx, *policy.Target.Repositories, diags).(types.Object)
	}
	target, dd := types.ObjectValue(targetType.AttrTypes, map[string]attr.Value{
		"repositories": repositories,
		"branches":     patternsValue(ctx, policy.Target.Branches, diags),
	})
	diags.Append(dd...)
	value, dd := types.ObjectValue(mergeQueueAppliedPolicyType.AttrTypes, map[string]attr.Value{
		"id":         types.StringValue(policy.ID),
		"name":       types.StringValue(policy.Name),
		"repository": types.StringPointerValue(policy.Repository),
		"target":     target,
		"settings":   settingsValue(policy.Settings, diags),
	})
	diags.Append(dd...)
	return value
}

func (d *MergeQueueBranchSettingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	defer d.client.warnOfSunset(&resp.Diagnostics)
	var data MergeQueueBranchSettingsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repository, branch := data.Owner.ValueString(), data.Repository.ValueString(), data.Branch.ValueString()
	got, err := d.client.GetMergeQueueBranchSettings(ctx, owner, repository, branch)
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error reading the merge queue settings of a base branch", err.Error())
		return
	}

	diags := &resp.Diagnostics
	settings, dd := types.ObjectValue(map[string]attr.Type{"batch_pull_request": types.BoolType}, map[string]attr.Value{
		"batch_pull_request": types.BoolValue(got.Settings.BatchPullRequest),
	})
	diags.Append(dd...)

	decision := got.Decisions.BatchPullRequest
	policyIDs, dd := types.ListValueFrom(ctx, types.StringType, orEmpty(decision.PolicyIDs))
	diags.Append(dd...)
	batchPullRequestDecision, dd := types.ObjectValue(mergeQueueSettingDecisionType.AttrTypes, map[string]attr.Value{
		"rule":       types.StringValue(decision.Rule),
		"policy_ids": policyIDs,
	})
	diags.Append(dd...)
	decisions, dd := types.ObjectValue(mergeQueueSettingDecisionsType.AttrTypes, map[string]attr.Value{
		"batch_pull_request": batchPullRequestDecision,
	})
	diags.Append(dd...)

	policyValues := make([]attr.Value, 0, len(got.Policies))
	for _, policy := range got.Policies {
		policyValues = append(policyValues, appliedPolicyValue(ctx, policy, diags))
	}
	policies, dd := types.ListValue(mergeQueueAppliedPolicyType, policyValues)
	diags.Append(dd...)
	if diags.HasError() {
		return
	}

	data.ID = types.StringValue(owner + "/" + repository + ":" + branch)
	data.Settings = settings
	data.Decisions = decisions
	data.Policies = policies
	diags.Append(resp.State.Set(ctx, &data)...)
}
