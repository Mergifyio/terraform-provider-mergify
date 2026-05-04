package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*OrganizationDefaultProductsResource)(nil)
	_ resource.ResourceWithConfigure   = (*OrganizationDefaultProductsResource)(nil)
	_ resource.ResourceWithImportState = (*OrganizationDefaultProductsResource)(nil)
)

type OrganizationDefaultProductsResource struct {
	client *Client
}

type OrganizationDefaultProductsResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Products     types.Set    `tfsdk:"products"`
}

func NewOrganizationDefaultProductsResource() resource.Resource {
	return &OrganizationDefaultProductsResource{}
}

func (r *OrganizationDefaultProductsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_default_products"
}

func (r *OrganizationDefaultProductsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage the default Mergify products enabled on new repositories of a GitHub organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource identifier — the organization name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization": schema.StringAttribute{
				Required:    true,
				Description: "GitHub organization (or user) the defaults apply to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"products": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Mergify products enabled by default on new repositories (e.g. `merge_queue`, `merge_protections`, `ci_insights`, `workflow_automation`).",
			},
		},
	}
}

func (r *OrganizationDefaultProductsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *OrganizationDefaultProductsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationDefaultProductsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	products := setToStrings(ctx, plan.Products, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetDefaultProducts(ctx, plan.Organization.ValueString(), products); err != nil {
		resp.Diagnostics.AddError("Mergify API error setting default products", err.Error())
		return
	}

	plan.ID = plan.Organization
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationDefaultProductsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationDefaultProductsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	products, err := r.client.GetDefaultProducts(ctx, state.Organization.ValueString())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Mergify API error reading default products", err.Error())
		return
	}

	productSet, diags := types.SetValueFrom(ctx, types.StringType, products)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Products = productSet
	state.ID = state.Organization
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationDefaultProductsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan OrganizationDefaultProductsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	products := setToStrings(ctx, plan.Products, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetDefaultProducts(ctx, plan.Organization.ValueString(), products); err != nil {
		resp.Diagnostics.AddError("Mergify API error updating default products", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationDefaultProductsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationDefaultProductsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetDefaultProducts(ctx, state.Organization.ValueString(), nil); err != nil {
		resp.Diagnostics.AddError("Mergify API error clearing default products", err.Error())
		return
	}
}

func (r *OrganizationDefaultProductsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected import ID to be the organization name, got empty string")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
