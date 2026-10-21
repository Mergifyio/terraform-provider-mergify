package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*PipelineResource)(nil)
	_ resource.ResourceWithConfigure   = (*PipelineResource)(nil)
	_ resource.ResourceWithImportState = (*PipelineResource)(nil)
)

type PipelineResource struct {
	client *Client
}

type pipelineResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Owner      types.String `tfsdk:"owner"`
	Repository types.String `tfsdk:"repository"`
	PipelineDefinition
}

// requiresReplaceIgnoringCase replaces the pipeline when its owner or
// repository changes, but not when only the case does: GitHub names are
// case-insensitive, and an import spelled otherwise than the configuration
// would delete and recreate the pipeline.
var requiresReplaceIgnoringCase = stringplanmodifier.RequiresReplaceIf(
	func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !strings.EqualFold(req.StateValue.ValueString(), req.PlanValue.ValueString())
	},
	"Changing this attribute, other than its case, forces resource replacement.",
	"Changing this attribute, other than its case, forces resource replacement.",
)

func NewPipelineResource() resource.Resource {
	return &PipelineResource{}
}

func (r *PipelineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline"
}

func optionalString(description string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Description: description}
}

func (r *PipelineResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	trigger := schema.NestedAttributeObject{
		Attributes: map[string]schema.Attribute{
			"context": schema.StringAttribute{
				Required:    true,
				Description: "`pull_request`: a pull request is opened, reopened or pushed to. `merge_queue`: the merge queue checks a batch of pull requests.",
			},
			"branches": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "`pull_request` only: run only for a pull request whose target branch matches one of these patterns, where `*` matches any run of characters, `/` included. Omitted: whatever its target branch.",
			},
			"queues": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "`merge_queue` only: run only for the batches of these queues. Omitted: every queue.",
			},
		},
	}
	// The triggers the API gives a pipeline whose definition has none.
	defaultTriggers, diags := types.SetValueFrom(ctx, trigger.Type(), []PipelineTrigger{
		{Context: "pull_request"},
		{Context: "merge_queue"},
	})
	resp.Diagnostics.Append(diags...)

	resp.Schema = schema.Schema{
		Description: "Define a Job Engine pipeline of a GitHub repository: what triggers it, the initial job each of its runs starts with, and the Vault its jobs read their secrets from.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "ID of the pipeline.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "GitHub organization or user that owns the repository.",
				PlanModifiers: []planmodifier.String{
					requiresReplaceIgnoringCase,
				},
			},
			"repository": schema.StringAttribute{
				Required:    true,
				Description: "GitHub repository name.",
				PlanModifiers: []planmodifier.String{
					requiresReplaceIgnoringCase,
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the pipeline, unique in its repository whatever the case. Each run of the pipeline posts the check `Mergify Job Engine / <name>`: renaming a pipeline renames its check.",
			},
			"triggers": schema.SetNestedAttribute{
				Optional:     true,
				Computed:     true,
				Default:      setdefault.StaticValue(defaultTriggers),
				Description:  "What runs the pipeline, one trigger per context. Omitted: a pull request or a merge queue batch, whatever its branch or queue. Empty: nothing runs it.",
				NestedObject: trigger,
			},
			"initial_job": schema.SingleNestedAttribute{
				Required:    true,
				Description: "The first job of each run, which writes the job list of the rest.",
				Attributes: map[string]schema.Attribute{
					"image": schema.StringAttribute{
						Required:    true,
						Description: "Docker image the initial job runs on the run's head commit.",
					},
					"args": schema.ListAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "Replace the image's CMD and keep its ENTRYPOINT, as `docker run <image> <args>` does. Omitted: the image's CMD. They appear in the run's logs: no secret here.",
					},
					"runs_on": optionalString("Runner label the initial job runs on. Omitted: the default runner of the repository's pipeline workflow."),
					"secrets": schema.MapNestedAttribute{
						Optional:    true,
						Description: "Secrets the initial job reads from `vault`, keyed by the name it receives each under: the file `/mergify-pipeline/secrets/<name>`, or the environment variable of that name. The jobs of the run declare their own in the job list.",
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"path": schema.StringAttribute{
									Required:    true,
									Description: "Vault path to read, as Vault's HTTP API names it: `secret/data/ci/registry` for a KV version 2 secret.",
								},
								"field": schema.StringAttribute{
									Required:    true,
									Description: "Field of the secret's data to hand over.",
								},
								"role": optionalString("Vault role to log in with. Omitted: `vault.default_role`."),
								"env": schema.BoolAttribute{
									Optional:    true,
									Description: "Hand the secret over as an environment variable rather than as a file. Anyone who can inspect the container on its runner can read its environment.",
								},
							},
						},
					},
				},
			},
			"vault": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Where the jobs of the pipeline's runs read their secrets. Omitted: they read none. None of it is a credential: the Vault roles' bound claims decide on the token Mergify signs for each job.",
				Attributes: map[string]schema.Attribute{
					"address": schema.StringAttribute{
						Required:    true,
						Description: "`https://` URL of the Vault server.",
					},
					"audience": schema.StringAttribute{
						Required:    true,
						Description: "Audience of the tokens Mergify signs for the pipeline's jobs, which the Vault roles' `bound_audiences` must name.",
					},
					"auth_mount":     optionalString("Path of Vault's JWT auth method. Omitted: `jwt`."),
					"namespace":      optionalString("Vault Enterprise namespace."),
					"ca_certificate": optionalString("PEM certificates the Vault server's certificate is checked against. Omitted: the runner's trusted ones. Mergify stores the certificates alone and drops a private key pasted along; the state keeps the text as configured."),
					"default_role":   optionalString("Vault role a secret that names none logs in with."),
				},
			},
		},
	}
}

func (r *PipelineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// pemBlock is a PEM block as the engine reads it: whatever text surrounds it,
// and whatever whitespace its lines hold. Go's pem.Decode wants each line flush
// left and each boundary on a line of its own, so an indented heredoc or a
// bundle joined without newlines would hold no certificate to it.
var pemBlock = regexp.MustCompile(`(?s)-----BEGIN ([A-Z0-9 ]+)-----(.*?)-----END ([A-Z0-9 ]+)-----`)

// sameCertificates tells whether two PEM texts hold the same certificates.
func sameCertificates(a, b string) bool {
	certificates := func(text string) []string {
		var out []string
		for _, m := range pemBlock.FindAllStringSubmatch(text, -1) {
			if m[1] == "CERTIFICATE" || m[1] == "X509 CERTIFICATE" {
				out = append(out, strings.Join(strings.Fields(m[2]), ""))
			}
		}
		return out
	}
	ours := certificates(a)
	return len(ours) > 0 && slices.Equal(ours, certificates(b))
}

func (r *PipelineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreatePipeline(ctx, plan.Owner.ValueString(), plan.Repository.ValueString(), plan.PipelineDefinition)
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error creating pipeline", err.Error())
		return
	}
	if created.ID == 0 {
		resp.Diagnostics.AddError("Mergify API error creating pipeline", "The API did not return the ID of the created pipeline.")
		return
	}

	// The response is not copied into the state: it carries the CA
	// certificate written anew, and Terraform refuses a value that differs
	// from the planned one.
	plan.ID = types.StringValue(strconv.FormatInt(created.ID, 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PipelineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Attribute by attribute: right after an import, the state holds nothing
	// else, and a null initial job does not fit the model.
	owner := getString(ctx, req.State, "owner", &resp.Diagnostics)
	repository := getString(ctx, req.State, "repository", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	var priorCertificate types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("vault").AtName("ca_certificate"), &priorCertificate)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := r.client.GetPipeline(ctx, owner, repository, id)
	if HasErrorCode(err, "PIPELINE_NOT_FOUND") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		// Any other 404 is not the pipeline's: a token that cannot see the
		// repository, an organization outside the Job Engine rollout.
		resp.Diagnostics.AddError("Mergify API error reading pipeline", err.Error())
		return
	}

	// The API stores the certificates alone, written anew: the prior text is
	// kept when it holds the same ones, as another spelling, or a private key
	// pasted along, would otherwise be a diff forever.
	if v := got.Vault; v != nil && v.CACertificate != nil && sameCertificates(priorCertificate.ValueString(), *v.CACertificate) {
		v.CACertificate = priorCertificate.ValueStringPointer()
	}
	// The API stores lists and accepts duplicates, which a Terraform set refuses.
	for i, t := range got.Triggers {
		if t.Branches != nil {
			got.Triggers[i].Branches = unique(t.Branches)
		}
		if t.Queues != nil {
			got.Triggers[i].Queues = unique(t.Queues)
		}
	}
	state := pipelineResourceModel{
		ID:                 types.StringValue(strconv.FormatInt(got.ID, 10)),
		Owner:              types.StringValue(owner),
		Repository:         types.StringValue(repository),
		PipelineDefinition: got.PipelineDefinition,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PipelineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan pipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdatePipeline(ctx, plan.Owner.ValueString(), plan.Repository.ValueString(), plan.ID.ValueString(), plan.PipelineDefinition); err != nil {
		resp.Diagnostics.AddError("Mergify API error updating pipeline", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PipelineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	owner := getString(ctx, req.State, "owner", &resp.Diagnostics)
	repository := getString(ctx, req.State, "repository", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeletePipeline(ctx, owner, repository, id); err != nil && !HasErrorCode(err, "PIPELINE_NOT_FOUND") {
		resp.Diagnostics.AddError("Mergify API error deleting pipeline", err.Error())
	}
}

func (r *PipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected import ID in the form `<owner>/<repository>/<id>`, got: "+req.ID,
		)
		return
	}
	if _, err := strconv.ParseInt(parts[2], 10, 64); err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"The pipeline ID is a number, got: "+parts[2],
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}
