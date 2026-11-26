package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

var (
	_ resource.Resource                = (*OIDCTrustPolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*OIDCTrustPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*OIDCTrustPolicyResource)(nil)
)

const oidcTrustPolicyDescription = "Manage an OIDC trust policy of an organization: which GitHub Actions jobs get a " +
	"Mergify OIDC CI token from their ID token, and what that token may do on the job's repository. " +
	"A job gets a token when one policy of its organization matches it, and a policy matches when each of its " +
	"lines does: repositories, events, workflows and environments. Policies only allow: there is no order and no " +
	"deny, so a strict policy cannot hold back what a broad one allows."

const githubActionsProvider = "github_actions"

// anyEvent, listed alone in `events`, matches every event: the API's `any`,
// which no GitHub event is named.
const anyEvent = "any"

// everyResource grants its level on every resource but `ci_reporting`.
const everyResource = "*"

var ciEvents = []string{"push", "pull_request", "merge_group", "schedule", "workflow_dispatch"}

var repositoriesModes = []string{"all", "all_except", "only", "visibility"}

var repositoryVisibilities = []string{"public", "internal", "private"}

var permissionLevels = []string{"read", "write"}

// ciOnlyPermissions is what a policy grants unless told otherwise, and all
// that an application key may make it grant.
var ciOnlyPermissions = map[string]string{"ci_reporting": "write"}

var trustPolicyRepositoriesType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"mode":         types.StringType,
	"ids":          types.SetType{ElemType: types.Int64Type},
	"visibilities": types.SetType{ElemType: types.StringType},
}}

var trustPolicyWorkflowType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"path":          types.StringType,
	"repository_id": types.Int64Type,
	"ref":           types.StringType,
}}

var githubActionsType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"events":       types.SetType{ElemType: types.StringType},
	"workflows":    types.SetType{ElemType: trustPolicyWorkflowType},
	"environments": types.SetType{ElemType: types.StringType},
}}

type oidcTrustPolicyModel struct {
	ID            types.String `tfsdk:"id"`
	Owner         types.String `tfsdk:"owner"`
	Name          types.String `tfsdk:"name"`
	Repositories  types.Object `tfsdk:"repositories"`
	GitHubActions types.Object `tfsdk:"github_actions"`
	Permissions   types.Map    `tfsdk:"permissions"`
}

type trustPolicyRepositoriesModel struct {
	Mode         types.String `tfsdk:"mode"`
	IDs          types.Set    `tfsdk:"ids"`
	Visibilities types.Set    `tfsdk:"visibilities"`
}

type githubActionsModel struct {
	Events       types.Set `tfsdk:"events"`
	Workflows    types.Set `tfsdk:"workflows"`
	Environments types.Set `tfsdk:"environments"`
}

type trustPolicyWorkflowModel struct {
	Path         types.String `tfsdk:"path"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Ref          types.String `tfsdk:"ref"`
}

type OIDCTrustPolicyResource struct {
	client *Client
}

func NewOIDCTrustPolicyResource() resource.Resource {
	return &OIDCTrustPolicyResource{}
}

func (r *OIDCTrustPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_oidc_trust_policy"
}

func (r *OIDCTrustPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: oidcTrustPolicyDescription,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The id of the policy.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"owner": requiredReplacingString("GitHub organization holding the policy."),
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The policy's name, unique among the organization's trust policies.",
			},
			"repositories": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				Default: objectdefault.StaticValue(types.ObjectValueMust(trustPolicyRepositoriesType.AttrTypes, map[string]attr.Value{
					"mode":         types.StringValue("all"),
					"ids":          types.SetNull(types.Int64Type),
					"visibilities": types.SetNull(types.StringType),
				})),
				Description: "The repositories whose jobs the policy matches. Defaults to every repository Mergify's GitHub App can access, new ones included.",
				Validators:  []validator.Object{repositoriesForMode{}},
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						Required: true,
						Description: "`all`: every repository Mergify's GitHub App can access, new ones included as they are added. " +
							"`only` the repositories of `ids`, or `all_except` them, new ones included. " +
							"`visibility`: every repository whose visibility is one of `visibilities`, new ones included.",
						Validators: []validator.String{stringOneOf(repositoriesModes)},
					},
					"ids": schema.SetAttribute{
						Optional:    true,
						ElementType: types.Int64Type,
						Description: "The GitHub ids of the repositories, for the `only` and `all_except` modes: the `repo_id` of the GitHub provider's `github_repository`.",
						Validators:  []validator.Set{setNotEmpty{"List at least one repository id."}},
					},
					"visibilities": schema.SetAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "`public`, `internal` or `private`, for the `visibility` mode: matched against the visibility " +
							"the job's ID token gives its repository when the job runs. A job whose ID token gives none is not matched.",
						Validators: []validator.Set{
							setNotEmpty{"List at least one visibility."},
							setOfOneOf(repositoryVisibilities),
						},
					},
				},
			},
			"github_actions": schema.SingleNestedAttribute{
				Required: true,
				Description: "What the policy matches of a GitHub Actions job, besides its repository: its events, " +
					"workflows and environments. `{}` matches the jobs started by the CI events, running any workflow, in any environment.",
				Attributes: map[string]schema.Attribute{
					"events": schema.SetAttribute{
						Optional:    true,
						Computed:    true,
						ElementType: types.StringType,
						Default:     setdefault.StaticValue(stringSet(ciEvents)),
						Description: "The job's `event_name`, or `[\"any\"]` for any event. Defaults to the CI events: " +
							"`push`, `pull_request`, `merge_group`, `schedule` and `workflow_dispatch`. " +
							"`pull_request_target`, `workflow_run` and `issue_comment` run with the repository's identity " +
							"on behalf of people who cannot push to it: they stay out unless listed.",
						Validators: []validator.Set{
							setNotEmpty{"List at least one event, or `any`."},
							anyEventAlone{},
						},
					},
					"workflows": schema.SetNestedAttribute{
						Optional:    true,
						Description: "The workflow the job runs. Defaults to any.",
						Validators:  []validator.Set{setNotEmpty{"List at least one workflow, or leave `workflows` out for any workflow."}},
						NestedObject: schema.NestedAttributeObject{
							Validators: []validator.Object{refPinsReusableWorkflow{}},
							Attributes: map[string]schema.Attribute{
								"path": schema.StringAttribute{
									Required:    true,
									Description: "The workflow file's path in its repository: `.github/workflows/ci.yml`.",
								},
								"repository_id": schema.Int64Attribute{
									Optional: true,
									Description: "For a reusable workflow the job runs, the GitHub id of the repository holding it, " +
										"one of the organization's that Mergify's GitHub App can access. Left out, the workflow " +
										"file is one of the job's own repository, which started the run.",
								},
								"ref": schema.StringAttribute{
									Optional: true,
									Description: "For a reusable workflow, the ref of it that matches: a ref (`refs/heads/main`, " +
										"`refs/tags/v1`) or a commit sha. Left out, any ref matches. A protected ref is the one way " +
										"to be sure that someone pushing a branch cannot edit the workflow that holds the token.",
								},
							},
						},
					},
					"environments": schema.SetAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "The job's environment. Defaults to any, which includes a job that runs in none.",
						Validators:  []validator.Set{setNotEmpty{"List at least one environment, or leave `environments` out for any environment."}},
					},
				},
			},
			"permissions": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Default:     mapdefault.StaticValue(stringMap(ciOnlyPermissions)),
				Description: "What the token of a job the policy matches may do on the job's own repository, resource by " +
					"resource, as an application key's permissions: `read`, or `write`, which includes read. `*` gives its " +
					"level on every resource but `ci_reporting`. A job several policies match gets what each of them grants. " +
					"Defaults to `{ ci_reporting = \"write\" }`, CI Reporting alone. An application key writes only the " +
					"policies granting CI Reporting alone: only an organization admin, signed in as themselves, writes, " +
					"changes or deletes one granting more.",
				Validators: []validator.Map{permissionsValidator{}},
			},
		},
	}
}

func (r *OIDCTrustPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// policy reads the policy the plan describes.
func (m oidcTrustPolicyModel) policy(ctx context.Context, diags *diag.Diagnostics) TrustPolicy {
	var repositories trustPolicyRepositoriesModel
	diags.Append(m.Repositories.As(ctx, &repositories, basetypes.ObjectAsOptions{})...)
	var lines githubActionsModel
	diags.Append(m.GitHubActions.As(ctx, &lines, basetypes.ObjectAsOptions{})...)

	events := setToStrings(ctx, lines.Events, diags)
	if slices.Equal(events, []string{anyEvent}) {
		events = nil
	}
	var workflows TrustPolicyLine[TrustPolicyWorkflow]
	for _, w := range setElements[trustPolicyWorkflowModel](ctx, lines.Workflows, diags) {
		workflows = append(workflows, TrustPolicyWorkflow{
			RepositoryID: w.RepositoryID.ValueInt64Pointer(),
			Path:         w.Path.ValueString(),
			Ref:          w.Ref.ValueStringPointer(),
		})
	}
	permissions := map[string]string{}
	diags.Append(m.Permissions.ElementsAs(ctx, &permissions, false)...)

	return TrustPolicy{
		Name:     m.Name.ValueString(),
		Provider: githubActionsProvider,
		Repositories: TrustPolicyRepositories{
			Mode:         repositories.Mode.ValueString(),
			IDs:          setElements[int64](ctx, repositories.IDs, diags),
			Visibilities: setToStrings(ctx, repositories.Visibilities, diags),
		},
		Conditions: GitHubActionsConditions{
			Events:       events,
			Workflows:    workflows,
			Environments: setToStrings(ctx, lines.Environments, diags),
		},
		Permissions: permissions,
	}
}

// trustPolicyState is the state of the policy the API answered.
func trustPolicyState(ctx context.Context, owner string, policy *TrustPolicy, diags *diag.Diagnostics) oidcTrustPolicyModel {
	if policy.Provider != githubActionsProvider {
		diags.AddError("Trust policy of another provider",
			"The policy trusts the jobs of `"+policy.Provider+"`, which this release of terraform-provider-mergify does not know: "+howToUpgrade)
	}
	repositories, d := types.ObjectValueFrom(ctx, trustPolicyRepositoriesType.AttrTypes, trustPolicyRepositoriesModel{
		Mode:         types.StringValue(policy.Repositories.Mode),
		IDs:          setFrom(ctx, types.Int64Type, policy.Repositories.IDs, diags),
		Visibilities: setFrom(ctx, types.StringType, policy.Repositories.Visibilities, diags),
	})
	diags.Append(d...)

	conditions := policy.Conditions
	events := []string(conditions.Events)
	if events == nil {
		events = []string{anyEvent}
	}
	var workflows []trustPolicyWorkflowModel
	for _, w := range conditions.Workflows {
		workflows = append(workflows, trustPolicyWorkflowModel{
			Path:         types.StringValue(w.Path),
			RepositoryID: types.Int64PointerValue(w.RepositoryID),
			Ref:          types.StringPointerValue(w.Ref),
		})
	}
	githubActions, d := types.ObjectValueFrom(ctx, githubActionsType.AttrTypes, githubActionsModel{
		Events:       setFrom(ctx, types.StringType, events, diags),
		Workflows:    setFrom(ctx, trustPolicyWorkflowType, workflows, diags),
		Environments: setFrom(ctx, types.StringType, conditions.Environments, diags),
	})
	diags.Append(d...)

	permissions := policy.Permissions
	if permissions == nil {
		permissions = ciOnlyPermissions
	}
	return oidcTrustPolicyModel{
		ID:            types.StringValue(strings.ToLower(policy.ID)),
		Owner:         types.StringValue(owner),
		Name:          types.StringValue(policy.Name),
		Repositories:  repositories,
		GitHubActions: githubActions,
		Permissions:   stringMap(permissions),
	}
}

func addTrustPolicyError(diags *diag.Diagnostics, action string, err error) {
	code, detail := APIErrorCode(err)
	switch code {
	case "CI_TRUST_POLICY_NAME_TAKEN":
		diags.AddAttributeError(path.Root("name"), "Trust policy name already taken", detail)
	case "CI_TRUST_POLICY_LIMIT_REACHED":
		diags.AddError("Too many trust policies", detail)
	case "CI_TRUST_POLICY_REPOSITORY_UNKNOWN":
		diags.AddError("Unknown repository in trust policy", detail)
	case "CI_TRUST_POLICY_GRANT_REFUSED":
		// What the policy grants already refuses a key any change, a
		// deletion too.
		diags.AddError("Trust policy permissions refused", detail)
	default:
		if refusesField(err, "permissions") {
			diags.AddAttributeError(path.Root("permissions"), "Permissions unknown to this Mergify",
				"This Mergify predates trust policy permissions: its policies grant CI Reporting alone. Leave `permissions` out, or upgrade Mergify.")
			return
		}
		diags.AddError("Mergify API error "+action+" trust policy", err.Error())
	}
}

func (r *OIDCTrustPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	defer r.client.warnOfSunset(&resp.Diagnostics)
	var plan oidcTrustPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policy := plan.policy(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// Left out, the policy grants CI reporting alone: a Mergify that
	// predates permissions refuses the field.
	if maps.Equal(policy.Permissions, ciOnlyPermissions) {
		policy.Permissions = nil
	}

	created, err := r.client.CreateTrustPolicy(ctx, plan.Owner.ValueString(), policy)
	if err != nil {
		addTrustPolicyError(&resp.Diagnostics, "creating", err)
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Mergify API error creating trust policy", "The API did not return the id of the created policy.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, trustPolicyState(ctx, plan.Owner.ValueString(), created, &resp.Diagnostics))...)
}

func (r *OIDCTrustPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	defer r.client.warnOfSunset(&resp.Diagnostics)
	owner := getString(ctx, req.State, "owner", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	policy, err := r.client.GetTrustPolicy(ctx, owner, id)
	if err != nil {
		if IsNotFound(err) {
			// The policy or its organization is gone.
			resp.State.RemoveResource(ctx)
			return
		}
		addTrustPolicyError(&resp.Diagnostics, "reading", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, trustPolicyState(ctx, owner, policy, &resp.Diagnostics))...)
}

func (r *OIDCTrustPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	defer r.client.warnOfSunset(&resp.Diagnostics)
	var plan oidcTrustPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	policy := plan.policy(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Permissions are sent whatever the policy granted when planned, so that
	// it grants what the plan says. A Mergify that predates them refuses the
	// field: it is sent the rest when the plan grants CI reporting alone, all
	// its policies grant.
	updated, err := r.client.UpdateTrustPolicy(ctx, plan.Owner.ValueString(), id, policy)
	if refusesField(err, "permissions") && maps.Equal(policy.Permissions, ciOnlyPermissions) {
		policy.Permissions = nil
		updated, err = r.client.UpdateTrustPolicy(ctx, plan.Owner.ValueString(), id, policy)
	}
	if err != nil {
		addTrustPolicyError(&resp.Diagnostics, "updating", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, trustPolicyState(ctx, plan.Owner.ValueString(), updated, &resp.Diagnostics))...)
}

func (r *OIDCTrustPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	defer r.client.warnOfSunset(&resp.Diagnostics)
	owner := getString(ctx, req.State, "owner", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteTrustPolicy(ctx, owner, id); err != nil && !IsNotFound(err) {
		addTrustPolicyError(&resp.Diagnostics, "deleting", err)
	}
}

func (r *OIDCTrustPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The import ID of an organization's merge queue policy has the same
	// form.
	scope, policyID, ok := organizationHolder.parseImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected import ID in the form `<owner>:<trust_policy_id>`, got: "+req.ID,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), scope.Owner)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), policyID)...)
}

// repositoriesForMode refuses `ids` or `visibilities` where the mode takes
// none, and their absence where it takes them.
type repositoriesForMode struct{}

func (repositoriesForMode) Description(context.Context) string {
	return "`ids` for the `only` and `all_except` modes, `visibilities` for the `visibility` mode, and nothing else"
}

func (v repositoriesForMode) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (repositoriesForMode) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attributes := req.ConfigValue.Attributes()
	mode, ok := attributes["mode"].(types.String)
	if !ok || mode.IsUnknown() || !slices.Contains(repositoriesModes, mode.ValueString()) {
		return
	}
	takes := map[string]string{"all_except": "ids", "only": "ids", "visibility": "visibilities"}[mode.ValueString()]
	for _, name := range []string{"ids", "visibilities"} {
		value := attributes[name]
		switch {
		case value.IsUnknown():
		case name == takes && value.IsNull():
			resp.Diagnostics.AddAttributeError(req.Path.AtName(name), "Missing "+name,
				fmt.Sprintf("The `%s` mode lists its repositories in `%s`.", mode.ValueString(), name))
		case name != takes && !value.IsNull():
			resp.Diagnostics.AddAttributeError(req.Path.AtName(name), "Unexpected "+name,
				fmt.Sprintf("The `%s` mode takes no `%s`.", mode.ValueString(), name))
		}
	}
}

// setOfOneOf refuses a set holding a string that is not one of values.
type setOfOneOf []string

func (v setOfOneOf) Description(ctx context.Context) string {
	return "each element " + stringOneOf(v).Description(ctx)
}

func (v setOfOneOf) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v setOfOneOf) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, element := range req.ConfigValue.Elements() {
		if value, ok := element.(types.String); ok {
			stringOneOf(v).check(ctx, req.Path.AtSetValue(element), value, &resp.Diagnostics)
		}
	}
}

// check validates value as an attribute at p would be.
func (v stringOneOf) check(ctx context.Context, p path.Path, value types.String, diags *diag.Diagnostics) {
	var resp validator.StringResponse
	v.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: value}, &resp)
	diags.Append(resp.Diagnostics...)
}

// anyEventAlone refuses `any` listed with other events, which it matches
// already.
type anyEventAlone struct{}

func (anyEventAlone) Description(context.Context) string {
	return "`any` is listed alone"
}

func (v anyEventAlone) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (anyEventAlone) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elements := req.ConfigValue.Elements()
	if len(elements) > 1 && slices.ContainsFunc(elements, types.StringValue(anyEvent).Equal) {
		resp.Diagnostics.AddAttributeError(req.Path, "Events listed with any",
			"`any` matches every event: list it alone.")
	}
}

// refPinsReusableWorkflow refuses a `ref` on a workflow of the job's own
// repository.
type refPinsReusableWorkflow struct{}

func (refPinsReusableWorkflow) Description(context.Context) string {
	return "`ref` is set only with `repository_id`"
}

func (v refPinsReusableWorkflow) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (refPinsReusableWorkflow) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	attributes := req.ConfigValue.Attributes()
	ref, repositoryID := attributes["ref"], attributes["repository_id"]
	if !ref.IsNull() && !ref.IsUnknown() && repositoryID.IsNull() {
		resp.Diagnostics.AddAttributeError(req.Path.AtName("ref"), "Ref without repository_id",
			"`ref` pins a reusable workflow: set its `repository_id` too. A workflow of the job's own repository matches whatever its ref.")
	}
}

// permissionsValidator refuses a level that is not one, no permission at
// all, and a permission that `*` grants already, which the API would leave
// out of the policy.
type permissionsValidator struct{}

func (permissionsValidator) Description(context.Context) string {
	return "at least one permission, each `read` or `write`, none that `*` grants already"
}

func (v permissionsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (permissionsValidator) ValidateMap(ctx context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elements := req.ConfigValue.Elements()
	if len(elements) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "No permission",
			"Grant at least one permission, or leave `permissions` out for CI Reporting alone.")
		return
	}
	// The level of a grantee, when it is known and one.
	level := func(grantee string) (string, bool) {
		value, ok := elements[grantee].(types.String)
		if !ok || value.IsUnknown() || !slices.Contains(permissionLevels, value.ValueString()) {
			return "", false
		}
		return value.ValueString(), true
	}
	every, everyKnown := level(everyResource)
	for _, grantee := range slices.Sorted(maps.Keys(elements)) {
		if value, ok := elements[grantee].(types.String); ok {
			stringOneOf(permissionLevels).check(ctx, req.Path.AtMapKey(grantee), value, &resp.Diagnostics)
		}
		access, known := level(grantee)
		if known && everyKnown && grantee != everyResource && grantee != "ci_reporting" && (every == "write" || access == "read") {
			resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(grantee), "Permission granted twice",
				fmt.Sprintf("`*` grants `%s` at `%s` already: leave it out.", grantee, access))
		}
	}
}
