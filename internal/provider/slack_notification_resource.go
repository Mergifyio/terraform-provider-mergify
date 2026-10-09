package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*SlackNotificationResource)(nil)
	_ resource.ResourceWithConfigure   = (*SlackNotificationResource)(nil)
	_ resource.ResourceWithImportState = (*SlackNotificationResource)(nil)
)

type slackNotificationFilter struct {
	name        string
	description string
}

// slackNotificationProduct describes one product of the Slack notification
// API. The products differ only by their filters, so a single resource
// implementation serves all of them.
type slackNotificationProduct struct {
	typeSuffix  string
	apiName     string
	responseKey string
	label       string
	filters     []slackNotificationFilter
}

var (
	slackMergeQueueNotification = slackNotificationProduct{
		typeSuffix:  "_slack_merge_queue_notification",
		apiName:     "merge_queue_notification",
		responseKey: "merge_queue_configs",
		label:       "merge queue",
		filters: []slackNotificationFilter{
			{"event_types", "Merge queue events posted to the channel (e.g. `action.queue.merged`, `queue.pause.create`). Empty means every event."},
		},
	}
	slackCIInsightsNotification = slackNotificationProduct{
		typeSuffix:  "_slack_ci_insights_notification",
		apiName:     "ci_insights_notification",
		responseKey: "ci_insights_configs",
		label:       "CI Insights",
		filters: []slackNotificationFilter{
			{"branches", "Branches whose CI jobs are reported. Empty means every branch."},
			{"pipeline_names", "CI pipelines whose jobs are reported. Empty means every pipeline."},
			{"job_names", "CI jobs that are reported. Empty means every job."},
			{"conclusions", "Job conclusions that are reported (e.g. `failure`). Empty means every conclusion."},
		},
	}
	slackTestQuarantineNotification = slackNotificationProduct{
		typeSuffix:  "_slack_test_quarantine_notification",
		apiName:     "test_quarantine_notification",
		responseKey: "test_quarantine_configs",
		label:       "test quarantine",
		filters: []slackNotificationFilter{
			{"event_types", "Test quarantine events posted to the channel: `test.quarantined`, `test.dequarantined`. Empty means every event."},
		},
	}
)

type SlackNotificationResource struct {
	client  *Client
	product slackNotificationProduct
}

func NewSlackMergeQueueNotificationResource() resource.Resource {
	return &SlackNotificationResource{product: slackMergeQueueNotification}
}

func NewSlackCIInsightsNotificationResource() resource.Resource {
	return &SlackNotificationResource{product: slackCIInsightsNotification}
}

func NewSlackTestQuarantineNotificationResource() resource.Resource {
	return &SlackNotificationResource{product: slackTestQuarantineNotification}
}

func (r *SlackNotificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.product.typeSuffix
}

func emptyStringSetAttribute(description string) schema.SetAttribute {
	return schema.SetAttribute{
		Optional:    true,
		Computed:    true,
		ElementType: types.StringType,
		Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})),
		Description: description,
	}
}

func (r *SlackNotificationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:    true,
			Description: "ID of the notification configuration.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"owner": schema.StringAttribute{
			Required:    true,
			Description: "GitHub organization the Slack channel is connected to.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"channel_id": schema.StringAttribute{
			Required:    true,
			Description: "ID of the Slack channel, from the `mergify_slack_channel` data source.",
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"repositories": emptyStringSetAttribute("Names of the organization's repositories to notify about. Empty means every repository."),
	}
	for _, f := range r.product.filters {
		attributes[f.name] = emptyStringSetAttribute(f.description)
	}
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("Post %s notifications to a Slack channel connected to a Mergify organization. Every filter left empty means no filtering on it.", r.product.label),
		Attributes:  attributes,
	}
}

func (r *SlackNotificationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type attributeGetter interface {
	GetAttribute(ctx context.Context, p path.Path, target any) diag.Diagnostics
}

func getString(ctx context.Context, src attributeGetter, name string, diags *diag.Diagnostics) string {
	var v types.String
	diags.Append(src.GetAttribute(ctx, path.Root(name), &v)...)
	return v.ValueString()
}

func (r *SlackNotificationResource) body(ctx context.Context, src attributeGetter, diags *diag.Diagnostics) SlackNotificationBody {
	body := SlackNotificationBody{}
	names := []string{"repositories"}
	for _, f := range r.product.filters {
		names = append(names, f.name)
	}
	for _, name := range names {
		var set types.Set
		diags.Append(src.GetAttribute(ctx, path.Root(name), &set)...)
		body[name] = orEmpty(setToStrings(ctx, set, diags))
	}
	return body
}

func (r *SlackNotificationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	owner := getString(ctx, req.Plan, "owner", &resp.Diagnostics)
	channelID := getString(ctx, req.Plan, "channel_id", &resp.Diagnostics)
	body := r.body(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateSlackNotification(ctx, owner, channelID, r.product.apiName, body)
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error creating Slack notification configuration", err.Error())
		return
	}

	// The response is not copied into the state: it carries repository
	// names in GitHub's casing, and Terraform refuses a value that differs
	// from the configured one.
	if created.ID == "" {
		resp.Diagnostics.AddError("Mergify API error creating Slack notification configuration", "The API did not return the ID of the created configuration.")
		return
	}
	resp.State.Raw = req.Plan.Raw
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.ID)...)
}

func (r *SlackNotificationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	owner := getString(ctx, req.State, "owner", &resp.Diagnostics)
	channelID := getString(ctx, req.State, "channel_id", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	channels, err := r.client.ListSlackChannels(ctx, owner)
	if IsNotFound(err) {
		// The organization is gone or Mergify is uninstalled from it.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error reading Slack notification configuration", err.Error())
		return
	}
	var notification *SlackNotification
	for i := range channels {
		// UUIDs: the API answers in lowercase whatever spelling it was given.
		if !strings.EqualFold(channels[i].ID, channelID) {
			continue
		}
		notification, err = channels[i].Notification(r.product.responseKey, id)
		if err != nil {
			resp.Diagnostics.AddError("Mergify API error reading Slack notification configuration", err.Error())
			return
		}
	}
	if notification == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	var priorRepositories types.Set
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("repositories"), &priorRepositories)...)
	values := map[string][]string{
		"repositories": keepCasing(setToStrings(ctx, priorRepositories, &resp.Diagnostics), notification.Repositories),
	}
	for _, f := range r.product.filters {
		values[f.name] = notification.Filters[f.name]
	}
	for name, v := range values {
		set, diags := types.SetValueFrom(ctx, types.StringType, unique(v))
		resp.Diagnostics.Append(diags...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), set)...)
	}
}

// keepCasing returns got with each name spelled as in prior when they only
// differ by case: GitHub repository names are case-insensitive and the API
// answers with GitHub's casing, which would otherwise show as a diff forever.
func keepCasing(prior, got []string) []string {
	out := make([]string, 0, len(got))
	for _, name := range got {
		for _, p := range prior {
			if strings.EqualFold(p, name) {
				name = p
				break
			}
		}
		out = append(out, name)
	}
	return out
}

func (r *SlackNotificationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	owner := getString(ctx, req.Plan, "owner", &resp.Diagnostics)
	channelID := getString(ctx, req.Plan, "channel_id", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	body := r.body(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateSlackNotification(ctx, owner, channelID, r.product.apiName, id, body); err != nil {
		resp.Diagnostics.AddError("Mergify API error updating Slack notification configuration", err.Error())
		return
	}

	resp.State.Raw = req.Plan.Raw
}

func (r *SlackNotificationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	owner := getString(ctx, req.State, "owner", &resp.Diagnostics)
	channelID := getString(ctx, req.State, "channel_id", &resp.Diagnostics)
	id := getString(ctx, req.State, "id", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteSlackNotification(ctx, owner, channelID, r.product.apiName, id); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Mergify API error deleting Slack notification configuration", err.Error())
	}
}

func (r *SlackNotificationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Expected import ID in the form `<owner>/<channel_id>/<id>`, got: "+req.ID,
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("channel_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}
