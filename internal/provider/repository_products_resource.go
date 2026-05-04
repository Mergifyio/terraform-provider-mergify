package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*RepositoryProductsResource)(nil)
	_ resource.ResourceWithConfigure   = (*RepositoryProductsResource)(nil)
	_ resource.ResourceWithImportState = (*RepositoryProductsResource)(nil)
)

type RepositoryProductsResource struct {
	client *Client
}

type RepositoryProductsResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Owner      types.String `tfsdk:"owner"`
	Repository types.String `tfsdk:"repository"`
	Products   types.Set    `tfsdk:"products"`
}

func NewRepositoryProductsResource() resource.Resource {
	return &RepositoryProductsResource{}
}

func (r *RepositoryProductsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_products"
}

func (r *RepositoryProductsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage which Mergify products are enabled on a GitHub repository.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource identifier in the form `<owner>/<repository>`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "GitHub organization or user that owns the repository.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"repository": schema.StringAttribute{
				Required:    true,
				Description: "GitHub repository name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"products": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Mergify products to enable on the repository (e.g. `merge_queue`, `merge_protections`, `ci_insights`).",
			},
		},
	}
}

func (r *RepositoryProductsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *RepositoryProductsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RepositoryProductsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	products := setToStrings(ctx, plan.Products, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetRepositoryProducts(ctx, plan.Owner.ValueString(), plan.Repository.ValueString(), products); err != nil {
		resp.Diagnostics.AddError("Mergify API error setting repository products", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.Owner.ValueString() + "/" + plan.Repository.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryProductsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RepositoryProductsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	products, found, err := r.client.GetRepositoryProducts(ctx, state.Owner.ValueString(), state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error reading repository products", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	productSet, diags := types.SetValueFrom(ctx, types.StringType, products)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Products = productSet
	state.ID = types.StringValue(state.Owner.ValueString() + "/" + state.Repository.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RepositoryProductsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RepositoryProductsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	products := setToStrings(ctx, plan.Products, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetRepositoryProducts(ctx, plan.Owner.ValueString(), plan.Repository.ValueString(), products); err != nil {
		resp.Diagnostics.AddError("Mergify API error updating repository products", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryProductsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RepositoryProductsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetRepositoryProducts(ctx, state.Owner.ValueString(), state.Repository.ValueString(), nil); err != nil {
		resp.Diagnostics.AddError("Mergify API error disabling repository products", err.Error())
		return
	}
}

func (r *RepositoryProductsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected import ID in the form `<owner>/<repository>`, got: "+req.ID,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
