---
page_title: "mergify_slack_channel Data Source - mergify"
subcategory: ""
description: |-
  Look up a Slack channel connected to a Mergify organization.
---

# mergify_slack_channel (Data Source)

Look up a Slack channel connected to a Mergify organization, to attach
notification configurations to it with the `mergify_slack_*_notification`
resources.

Channels are connected from the Mergify dashboard, through Slack's
authorization flow. Terraform cannot connect or disconnect one.

Reading channels and managing their notifications requires a token with
the integrations admin role on the organization, such as an admin
application key.

## Example Usage

```terraform
data "mergify_slack_channel" "merge_queue" {
  owner = "Mergifyio"
  name  = "#merge-queue"
}
```

## Schema

### Required

- `owner` (String) GitHub organization the channel is connected to.
- `name` (String) Slack channel name, with or without the leading `#`.
  When a channel was reconnected after Mergify lost access to it, the
  working connection is picked over the disabled one.

### Optional

- `workspace` (String) Slack workspace name. Required only when channels
  with the same name are connected from several workspaces.

### Read-Only

- `id` (String) ID of the Slack channel configuration, to pass as
  `channel_id` to the `mergify_slack_*_notification` resources.
- `disabled_at` (String) When Mergify stopped being able to post to the
  channel, e.g. after the app was removed from the Slack workspace. Null
  while the channel receives notifications.
- `disabled_reason` (String) Why Mergify cannot post to the channel
  anymore. Null while the channel receives notifications.
