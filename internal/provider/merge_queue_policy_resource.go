package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*MergeQueuePolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*MergeQueuePolicyResource)(nil)
	_ resource.ResourceWithImportState = (*MergeQueuePolicyResource)(nil)
)

const batchPullRequestDescription = "Whether the merge queue opens a draft pull request to check a batch. " +
	"When `false`, Mergify pushes the batch to its queue branch and reads the CI of that branch's head commit, " +
	"with no pull request: the CI of the base branch must run on a `push` to the branches under the " +
	"`queue_branch_prefix` of your queue rules (`mergify/merge-queue/` by default). " +
	"A queue rule that uses `merge_method: fast-forward` or `allow_queue_branch_edit`, or " +
	"`merge_method: merge-batch` in the `isolated` mode or with a `merge_bot_account` in the `parallel` mode, " +
	"needs a draft pull request: unless its checks run in place, Mergify does not queue a pull request for " +
	"such a rule on a base branch where this is `false`."

const mergeQueuePolicyRule = "Every active merge queue policy whose target holds a base branch applies to it, " +
	"with no order between them. For each setting, a branch gets the default when no policy that applies sets it, " +
	"their value when they agree, and the safest value when they disagree: for `batch_pull_request`, `true`, " +
	"a draft pull request, which works with any CI."

const patternDescription = "A name, or a glob pattern matching names as the `*=` condition operator does: " +
	"`*` matches within one path segment, `**` across `/`. `~ALL` matches every one."

const branchPatternDescription = patternDescription + " `~DEFAULT_BRANCH` matches the repository's default branch."

var enforcements = []string{"active", "disabled"}

var mergeQueuePolicyPatternsType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"include_patterns": types.SetType{ElemType: types.StringType},
	"exclude_patterns": types.SetType{ElemType: types.StringType},
}}

var mergeQueuePolicySettingsType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"batch_pull_request": types.BoolType,
}}

// mergeQueuePolicyHolder is what holds a merge queue policy: an organization,
// whose policies target its repositories and their base branches, or a
// repository, whose policies target its own base branches. The two differ
// only by the arguments naming the holder and the repositories of the
// target, so a single resource implementation serves both.
type mergeQueuePolicyHolder int

const (
	organizationHolder mergeQueuePolicyHolder = iota
	repositoryHolder
)

func (h mergeQueuePolicyHolder) typeSuffix() string {
	return [...]string{"_organization_merge_queue_policy", "_repository_merge_queue_policy"}[h]
}

func (h mergeQueuePolicyHolder) importIDForm() string {
	return [...]string{"<owner>:<policy_id>", "<owner>/<repository>:<policy_id>"}[h]
}

// parseImportID reads an import ID. A policy id is a UUID, and neither an
// owner nor a repository name holds a `:` or a `/`.
func (h mergeQueuePolicyHolder) parseImportID(id string) (MergeQueuePolicyScope, string, bool) {
	var scope MergeQueuePolicyScope
	holder, policyID, found := strings.Cut(id, ":")
	if !found || policyID == "" || strings.ContainsAny(policyID, ":/") {
		return scope, "", false
	}
	if h == organizationHolder {
		scope.Owner = holder
		return scope, policyID, scope.Owner != "" && !strings.Contains(scope.Owner, "/")
	}
	scope.Owner, scope.Repository, found = strings.Cut(holder, "/")
	return scope, policyID, found && scope.Owner != "" && scope.Repository != "" && !strings.Contains(scope.Repository, "/")
}

func (h mergeQueuePolicyHolder) targetType() types.ObjectType {
	attrTypes := map[string]attr.Type{"branches": mergeQueuePolicyPatternsType}
	if h == organizationHolder {
		attrTypes["repositories"] = mergeQueuePolicyPatternsType
	}
	return types.ObjectType{AttrTypes: attrTypes}
}

type MergeQueuePolicyResource struct {
	client *Client
	holder mergeQueuePolicyHolder
}

func NewOrganizationMergeQueuePolicyResource() resource.Resource {
	return &MergeQueuePolicyResource{holder: organizationHolder}
}

func NewRepositoryMergeQueuePolicyResource() resource.Resource {
	return &MergeQueuePolicyResource{holder: repositoryHolder}
}

func (r *MergeQueuePolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.holder.typeSuffix()
}

func requiredReplacingString(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Required:    true,
		Description: description,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
}

func patternsAttribute(description, patterns string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Required:    true,
		Description: description,
		Attributes: map[string]schema.Attribute{
			"include_patterns": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "The ones targeted, at least one. " + patterns,
				Validators:  []validator.Set{setNotEmpty{}},
			},
			"exclude_patterns": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
				Description: "The ones never targeted, even when included. " + patterns,
			},
		},
	}
}

func (r *MergeQueuePolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	targetAttributes := map[string]schema.Attribute{
		"branches": patternsAttribute("The base branches the policy applies to.", branchPatternDescription),
	}
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "The id of the policy.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"name": schema.StringAttribute{
			Required:    true,
			Description: "The policy's name, unique among the policies of its organization or repository.",
		},
		"enforcement": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString("active"),
			Description: "`active` applies the policy; `disabled` keeps it listed without applying it. Defaults to `active`.",
			Validators:  []validator.String{stringOneOf(enforcements)},
		},
		"settings": schema.SingleNestedAttribute{
			Required:    true,
			Description: "The merge queue settings the policy sets, at least one. A setting left out is not set by the policy.",
			Validators:  []validator.Object{setsSomething{}},
			Attributes: map[string]schema.Attribute{
				"batch_pull_request": schema.BoolAttribute{
					Optional:    true,
					Description: batchPullRequestDescription,
				},
			},
		},
	}
	var description string
	switch r.holder {
	case organizationHolder:
		description = "Manage a merge queue policy of an organization, which sets merge queue settings for the base branches of the repositories it targets. " + mergeQueuePolicyRule
		attributes["owner"] = requiredReplacingString("GitHub organization holding the policy.")
		targetAttributes["repositories"] = patternsAttribute("The repositories the policy applies to.", patternDescription)
	case repositoryHolder:
		description = "Manage a merge queue policy of a repository, which sets merge queue settings for the base branches of the repository it targets. " + mergeQueuePolicyRule
		attributes["owner"] = requiredReplacingString("GitHub organization or user that owns the repository.")
		attributes["repository"] = requiredReplacingString("GitHub repository holding the policy.")
	}
	attributes["target"] = schema.SingleNestedAttribute{
		Required:    true,
		Description: "What the policy applies to.",
		Attributes:  targetAttributes,
	}
	resp.Schema = schema.Schema{Description: description, Attributes: attributes}
}

func (r *MergeQueuePolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	r.client = client
}

func (r *MergeQueuePolicyResource) scope(ctx context.Context, from attributeGetter, diags *diag.Diagnostics) MergeQueuePolicyScope {
	scope := MergeQueuePolicyScope{Owner: getString(ctx, from, "owner", diags)}
	if r.holder == repositoryHolder {
		scope.Repository = getString(ctx, from, "repository", diags)
	}
	return scope
}

func getPatterns(ctx context.Context, from attributeGetter, at path.Path, diags *diag.Diagnostics) MergeQueuePolicyPatterns {
	var include, exclude types.Set
	diags.Append(from.GetAttribute(ctx, at.AtName("include_patterns"), &include)...)
	diags.Append(from.GetAttribute(ctx, at.AtName("exclude_patterns"), &exclude)...)
	return MergeQueuePolicyPatterns{
		IncludePatterns: orEmpty(setToStrings(ctx, include, diags)),
		ExcludePatterns: orEmpty(setToStrings(ctx, exclude, diags)),
	}
}

// policy reads the policy the plan describes.
func (r *MergeQueuePolicyResource) policy(ctx context.Context, plan attributeGetter, diags *diag.Diagnostics) MergeQueuePolicy {
	target := path.Root("target")
	policy := MergeQueuePolicy{
		Name:        getString(ctx, plan, "name", diags),
		Enforcement: getString(ctx, plan, "enforcement", diags),
		Target: MergeQueuePolicyTarget{
			Branches: getPatterns(ctx, plan, target.AtName("branches"), diags),
		},
	}
	if r.holder == organizationHolder {
		repositories := getPatterns(ctx, plan, target.AtName("repositories"), diags)
		policy.Target.Repositories = &repositories
	}
	var batchPullRequest types.Bool
	diags.Append(plan.GetAttribute(ctx, path.Root("settings").AtName("batch_pull_request"), &batchPullRequest)...)
	policy.Settings.BatchPullRequest = batchPullRequest.ValueBoolPointer()
	return policy
}

func patternsValue(ctx context.Context, patterns MergeQueuePolicyPatterns, diags *diag.Diagnostics) attr.Value {
	include, d := types.SetValueFrom(ctx, types.StringType, unique(patterns.IncludePatterns))
	diags.Append(d...)
	exclude, d := types.SetValueFrom(ctx, types.StringType, unique(patterns.ExcludePatterns))
	diags.Append(d...)
	value, d := types.ObjectValue(mergeQueuePolicyPatternsType.AttrTypes, map[string]attr.Value{
		"include_patterns": include,
		"exclude_patterns": exclude,
	})
	diags.Append(d...)
	return value
}

func settingsValue(settings MergeQueuePolicySettings, diags *diag.Diagnostics) attr.Value {
	value, d := types.ObjectValue(mergeQueuePolicySettingsType.AttrTypes, map[string]attr.Value{
		"batch_pull_request": types.BoolPointerValue(settings.BatchPullRequest),
	})
	diags.Append(d...)
	return value
}

// setState stores the policy as the API answered it.
func (r *MergeQueuePolicyResource) setState(ctx context.Context, policy *MergeQueuePolicy, state attributeSetter, diags *diag.Diagnostics) {
	targetValues := map[string]attr.Value{
		"branches": patternsValue(ctx, policy.Target.Branches, diags),
	}
	if r.holder == organizationHolder {
		var repositories MergeQueuePolicyPatterns
		if policy.Target.Repositories != nil {
			repositories = *policy.Target.Repositories
		}
		targetValues["repositories"] = patternsValue(ctx, repositories, diags)
	}
	target, d := types.ObjectValue(r.holder.targetType().AttrTypes, targetValues)
	diags.Append(d...)

	diags.Append(state.SetAttribute(ctx, path.Root("id"), strings.ToLower(policy.ID))...)
	diags.Append(state.SetAttribute(ctx, path.Root("name"), policy.Name)...)
	diags.Append(state.SetAttribute(ctx, path.Root("enforcement"), policy.Enforcement)...)
	diags.Append(state.SetAttribute(ctx, path.Root("target"), target)...)
	diags.Append(state.SetAttribute(ctx, path.Root("settings"), settingsValue(policy.Settings, diags))...)
}

type attributeSetter interface {
	SetAttribute(ctx context.Context, p path.Path, value any) diag.Diagnostics
}

func addMergeQueuePolicyError(diags *diag.Diagnostics, action string, err error) {
	code, detail := APIErrorCode(err)
	switch code {
	case "MERGE_QUEUE_SETTING_NOT_AVAILABLE":
		diags.AddAttributeError(path.Root("settings").AtName("batch_pull_request"), "batch_pull_request = false is not available to this organization", detail)
	case "MERGE_QUEUE_POLICY_INVALID_PATTERN":
		diags.AddAttributeError(path.Root("target"), "Invalid merge queue policy target pattern", detail)
	case "MERGE_QUEUE_POLICY_NAME_TAKEN":
		diags.AddAttributeError(path.Root("name"), "Merge queue policy name already taken", detail)
	case "MERGE_QUEUE_POLICY_LIMIT_REACHED":
		diags.AddError("Too many merge queue policies", detail)
	default:
		diags.AddError("Mergify API error "+action+" merge queue policy", err.Error())
	}
}

func (r *MergeQueuePolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	scope := r.scope(ctx, req.Plan, &resp.Diagnostics)
	policy := r.policy(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateMergeQueuePolicy(ctx, scope, policy)
	if err != nil {
		addMergeQueuePolicyError(&resp.Diagnostics, "creating", err)
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Mergify API error creating merge queue policy", "The API did not return the id of the created policy.")
		return
	}

	resp.State.Raw = req.Plan.Raw
	r.setState(ctx, created, &resp.State, &resp.Diagnostics)
}

func (r *MergeQueuePolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	scope := r.scope(ctx, req.State, &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	policy, err := r.client.GetMergeQueuePolicy(ctx, scope, id)
	if err != nil {
		if IsNotFound(err) {
			// The policy, its repository or its organization is gone.
			resp.State.RemoveResource(ctx)
			return
		}
		addMergeQueuePolicyError(&resp.Diagnostics, "reading", err)
		return
	}
	r.setState(ctx, policy, &resp.State, &resp.Diagnostics)
}

func (r *MergeQueuePolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	scope := r.scope(ctx, req.Plan, &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	policy := r.policy(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	updated, err := r.client.UpdateMergeQueuePolicy(ctx, scope, id, policy)
	if err != nil {
		addMergeQueuePolicyError(&resp.Diagnostics, "updating", err)
		return
	}

	resp.State.Raw = req.Plan.Raw
	r.setState(ctx, updated, &resp.State, &resp.Diagnostics)
}

func (r *MergeQueuePolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	scope := r.scope(ctx, req.State, &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteMergeQueuePolicy(ctx, scope, id); err != nil && !IsNotFound(err) {
		addMergeQueuePolicyError(&resp.Diagnostics, "deleting", err)
	}
}

func (r *MergeQueuePolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	scope, policyID, ok := r.holder.parseImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected import ID in the form `"+r.holder.importIDForm()+"`, got: "+req.ID,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), scope.Owner)...)
	if r.holder == repositoryHolder {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), scope.Repository)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), policyID)...)
}

// setNotEmpty refuses a set holding no element.
type setNotEmpty struct{}

func (setNotEmpty) Description(context.Context) string { return "must hold at least one element" }

func (v setNotEmpty) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (setNotEmpty) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Empty set", "Must hold at least one pattern.")
	}
}

// stringOneOf refuses a string that is not one of values.
type stringOneOf []string

func (v stringOneOf) Description(context.Context) string {
	return "must be one of `" + strings.Join(v, "`, `") + "`"
}

func (v stringOneOf) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v stringOneOf) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !slices.Contains(v, req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", "Got `"+req.ConfigValue.ValueString()+"`, "+v.Description(ctx)+".")
	}
}

// setsSomething refuses an object whose attributes are all null: a policy
// sets at least one setting.
type setsSomething struct{}

func (setsSomething) Description(context.Context) string { return "must set at least one setting" }

func (v setsSomething) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (setsSomething) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, value := range req.ConfigValue.Attributes() {
		if !value.IsNull() {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "No setting set", "A merge queue policy sets at least one setting.")
}
