package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*SlackChannelDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*SlackChannelDataSource)(nil)
)

type SlackChannelDataSource struct {
	client *Client
}

type SlackChannelDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Owner          types.String `tfsdk:"owner"`
	Name           types.String `tfsdk:"name"`
	Workspace      types.String `tfsdk:"workspace"`
	DisabledAt     types.String `tfsdk:"disabled_at"`
	DisabledReason types.String `tfsdk:"disabled_reason"`
}

func NewSlackChannelDataSource() datasource.DataSource {
	return &SlackChannelDataSource{}
}

func (d *SlackChannelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slack_channel"
}

func (d *SlackChannelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a Slack channel connected to a Mergify organization. Channels are connected from the Mergify dashboard; Terraform cannot connect one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "ID of the Slack channel configuration, to pass as `channel_id` to the `mergify_slack_*_notification` resources.",
			},
			"owner": schema.StringAttribute{
				Required:    true,
				Description: "GitHub organization the channel is connected to.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Slack channel name, with or without the leading `#`.",
			},
			"workspace": schema.StringAttribute{
				Optional:    true,
				Description: "Slack workspace name. Required only when channels with the same name are connected from several workspaces.",
			},
			"disabled_at": schema.StringAttribute{
				Computed:    true,
				Description: "When Mergify stopped being able to post to the channel. Null while the channel receives notifications.",
			},
			"disabled_reason": schema.StringAttribute{
				Computed:    true,
				Description: "Why Mergify cannot post to the channel anymore. Null while the channel receives notifications.",
			},
		},
	}
}

func (d *SlackChannelDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *SlackChannelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SlackChannelDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	channels, err := d.client.ListSlackChannels(ctx, data.Owner.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Mergify API error listing Slack channels", err.Error())
		return
	}

	name := strings.TrimPrefix(data.Name.ValueString(), "#")
	var matches []SlackChannel
	for _, ch := range channels {
		if strings.TrimPrefix(ch.SlackChannel, "#") != name {
			continue
		}
		if !data.Workspace.IsNull() && (ch.SlackTeamName == nil || *ch.SlackTeamName != data.Workspace.ValueString()) {
			continue
		}
		matches = append(matches, ch)
	}
	// Reconnecting a channel Mergify can no longer post to adds a new one and
	// leaves the disabled one behind: the one that works is the one wanted.
	var enabled []SlackChannel
	for _, ch := range matches {
		if ch.DisabledAt == nil {
			enabled = append(enabled, ch)
		}
	}
	if len(enabled) > 0 {
		matches = enabled
	}

	if len(matches) == 0 {
		resp.Diagnostics.AddError(
			"Slack channel not found",
			fmt.Sprintf("No Slack channel named %q%s is connected to %s. Connect it from the Mergify dashboard first.", data.Name.ValueString(), workspaceSuffix(data.Workspace), data.Owner.ValueString()),
		)
		return
	}
	if len(matches) > 1 {
		resp.Diagnostics.AddError(
			"Several Slack channels match",
			fmt.Sprintf("%d Slack channels named %q are connected to %s. Set `workspace` to pick one; if they are in the same workspace, disconnect the duplicates from the Mergify dashboard.", len(matches), data.Name.ValueString(), data.Owner.ValueString()),
		)
		return
	}

	ch := matches[0]
	data.ID = types.StringValue(ch.ID)
	data.DisabledAt = types.StringPointerValue(ch.DisabledAt)
	data.DisabledReason = types.StringPointerValue(ch.DisabledReason)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func workspaceSuffix(workspace types.String) string {
	if workspace.IsNull() {
		return ""
	}
	return fmt.Sprintf(" in workspace %q", workspace.ValueString())
}
