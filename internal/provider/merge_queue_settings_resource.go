package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*MergeQueueSettingsResource)(nil)
	_ resource.ResourceWithConfigure   = (*MergeQueueSettingsResource)(nil)
	_ resource.ResourceWithImportState = (*MergeQueueSettingsResource)(nil)
)

const batchPullRequestDescription = "Whether the merge queue opens a draft pull request to check a batch. " +
	"When `false`, Mergify pushes the batch to its queue branch and reads the CI of that branch's head commit, " +
	"with no pull request: the CI of the base branch must run on a `push` to the branches under the " +
	"`queue_branch_prefix` of your queue rules (`mergify/merge-queue/` by default). " +
	"A queue rule that needs a draft pull request, such as one using `merge_method: fast-forward` or " +
	"`allow_queue_branch_edit`, does not queue a pull request on a base branch where this is `false`, " +
	"unless its checks run in place. Unset leaves it to the level above."

const branchLevelDescription = "Base branch name, or a glob pattern matching base branch names as the `*=` " +
	"condition operator does (e.g. `release/*`, `stable/**`). A literal `{`, `}` or `,` in a branch name is " +
	"escaped with `\\`."

// mergeQueueSettingsScope is one of the levels holding merge queue settings.
// The levels differ only by the arguments naming them, so a single resource
// implementation serves all of them.
type mergeQueueSettingsScope int

const (
	organizationScope mergeQueueSettingsScope = iota
	repositoryScope
	branchScope
)

func (s mergeQueueSettingsScope) typeSuffix() string {
	return [...]string{
		"_organization_merge_queue_settings",
		"_repository_merge_queue_settings",
		"_branch_merge_queue_settings",
	}[s]
}

// importIDForm is the import ID, and the `id`, of a resource at this level.
func (s mergeQueueSettingsScope) importIDForm() string {
	return [...]string{"<owner>", "<owner>/<repository>", "<owner>/<repository>:<branch>"}[s]
}

func (s mergeQueueSettingsScope) id(level MergeQueueSettingsLevel) string {
	switch s {
	case organizationScope:
		return level.Owner
	case repositoryScope:
		return level.Owner + "/" + level.Repository
	default:
		return level.Owner + "/" + level.Repository + ":" + level.Branch
	}
}

// parseID reads an import ID. A branch name cannot hold a `:` (git refuses
// it in a ref), nor an owner or a repository name a `/`, so the first of each
// separates the arguments.
func (s mergeQueueSettingsScope) parseID(id string) (MergeQueueSettingsLevel, bool) {
	var level MergeQueueSettingsLevel
	rest := id
	if s == branchScope {
		var found bool
		rest, level.Branch, found = strings.Cut(id, ":")
		if !found || level.Branch == "" {
			return level, false
		}
	}
	if s == organizationScope {
		level.Owner = rest
		return level, level.Owner != "" && !strings.ContainsAny(level.Owner, "/:")
	}
	var found bool
	level.Owner, level.Repository, found = strings.Cut(rest, "/")
	return level, found && level.Owner != "" && level.Repository != "" && !strings.Contains(level.Repository, "/")
}

type MergeQueueSettingsResource struct {
	client *Client
	scope  mergeQueueSettingsScope
}

func NewOrganizationMergeQueueSettingsResource() resource.Resource {
	return &MergeQueueSettingsResource{scope: organizationScope}
}

func NewRepositoryMergeQueueSettingsResource() resource.Resource {
	return &MergeQueueSettingsResource{scope: repositoryScope}
}

func NewBranchMergeQueueSettingsResource() resource.Resource {
	return &MergeQueueSettingsResource{scope: branchScope}
}

func (r *MergeQueueSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.scope.typeSuffix()
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

func (r *MergeQueueSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "Resource identifier in the form `" + r.scope.importIDForm() + "`.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"batch_pull_request": schema.BoolAttribute{
			Optional:    true,
			Description: batchPullRequestDescription,
		},
	}
	var description string
	switch r.scope {
	case organizationScope:
		description = "Manage the merge queue settings an organization sets for its repositories. A repository or a base branch may override each value."
		attributes["owner"] = requiredReplacingString("GitHub organization.")
	case repositoryScope:
		description = "Manage the merge queue settings a repository sets for its base branches. A base branch may override each value, and a value the repository leaves unset comes from its organization."
		attributes["owner"] = requiredReplacingString("GitHub organization or user that owns the repository.")
		attributes["repository"] = requiredReplacingString("GitHub repository name.")
	case branchScope:
		description = "Manage the merge queue settings a base branch name or pattern sets in a repository. A value it leaves unset comes from the next pattern matching the branch, then from the repository."
		attributes["owner"] = requiredReplacingString("GitHub organization or user that owns the repository.")
		attributes["repository"] = requiredReplacingString("GitHub repository name.")
		attributes["branch"] = requiredReplacingString(branchLevelDescription)
	}
	resp.Schema = schema.Schema{Description: description, Attributes: attributes}
}

func (r *MergeQueueSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// attributeReader is a plan or a state.
type attributeReader interface {
	GetAttribute(ctx context.Context, p path.Path, target interface{}) diag.Diagnostics
}

// level reads the arguments naming the level, which the three schemas share
// only in part, so they are read one by one rather than into a model.
func (r *MergeQueueSettingsResource) level(ctx context.Context, from attributeReader, diags *diag.Diagnostics) MergeQueueSettingsLevel {
	var owner, repository, branch types.String
	diags.Append(from.GetAttribute(ctx, path.Root("owner"), &owner)...)
	if r.scope >= repositoryScope {
		diags.Append(from.GetAttribute(ctx, path.Root("repository"), &repository)...)
	}
	if r.scope == branchScope {
		diags.Append(from.GetAttribute(ctx, path.Root("branch"), &branch)...)
	}
	return MergeQueueSettingsLevel{
		Owner:      owner.ValueString(),
		Repository: repository.ValueString(),
		Branch:     branch.ValueString(),
	}
}

func addMergeQueueSettingsError(diags *diag.Diagnostics, action string, err error) {
	code, detail := APIErrorCode(err)
	switch code {
	case "MERGE_QUEUE_SETTING_NOT_AVAILABLE":
		diags.AddAttributeError(path.Root("batch_pull_request"), "batch_pull_request = false is not available to this organization", detail)
	case "MERGE_QUEUE_SETTING_INVALID_BRANCH_PATTERN":
		diags.AddAttributeError(path.Root("branch"), "Invalid base branch pattern", detail)
	default:
		diags.AddError("Mergify API error "+action+" merge queue settings", err.Error())
	}
}

// write replaces what the level sets with the plan, then stores what the API
// answers when read back.
func (r *MergeQueueSettingsResource) write(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics, action string) {
	level := r.level(ctx, plan, diags)
	var batchPullRequest types.Bool
	diags.Append(plan.GetAttribute(ctx, path.Root("batch_pull_request"), &batchPullRequest)...)
	if diags.HasError() {
		return
	}

	settings := MergeQueueSettings{BatchPullRequest: batchPullRequest.ValueBoolPointer()}
	if err := r.client.PutMergeQueueSettings(ctx, level, settings); err != nil {
		addMergeQueueSettingsError(diags, action, err)
		return
	}
	read, err := r.client.GetMergeQueueSettings(ctx, level)
	if err != nil {
		addMergeQueueSettingsError(diags, "reading", err)
		return
	}

	state.Raw = plan.Raw
	diags.Append(state.SetAttribute(ctx, path.Root("id"), r.scope.id(level))...)
	diags.Append(state.SetAttribute(ctx, path.Root("batch_pull_request"), types.BoolPointerValue(read.BatchPullRequest))...)
}

func (r *MergeQueueSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.write(ctx, req.Plan, &resp.State, &resp.Diagnostics, "setting")
}

func (r *MergeQueueSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.write(ctx, req.Plan, &resp.State, &resp.Diagnostics, "updating")
}

func (r *MergeQueueSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	level := r.level(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// A level that sets nothing still answers, with every value null; only
	// an organization or repository Mergify no longer knows is a 404.
	read, err := r.client.GetMergeQueueSettings(ctx, level)
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addMergeQueueSettingsError(&resp.Diagnostics, "reading", err)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), r.scope.id(level))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("batch_pull_request"), types.BoolPointerValue(read.BatchPullRequest))...)
}

func (r *MergeQueueSettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	level := r.level(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteMergeQueueSettings(ctx, level); err != nil && !IsNotFound(err) {
		addMergeQueueSettingsError(&resp.Diagnostics, "unsetting", err)
	}
}

func (r *MergeQueueSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	level, ok := r.scope.parseID(req.ID)
	if !ok {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected import ID in the form `"+r.scope.importIDForm()+"`, got: "+req.ID,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), level.Owner)...)
	if r.scope >= repositoryScope {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), level.Repository)...)
	}
	if r.scope == branchScope {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("branch"), level.Branch)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), r.scope.id(level))...)
}
