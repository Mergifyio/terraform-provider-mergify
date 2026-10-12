---
page_title: "mergify_slack_merge_queue_notification Resource - mergify"
subcategory: ""
description: |-
  Post merge queue notifications to a Slack channel connected to a Mergify organization.
---

# mergify_slack_merge_queue_notification (Resource)

Post merge queue notifications to a Slack channel connected to a Mergify
organization. Changing a filter updates the configuration in place.

The channel itself must already be connected from the Mergify dashboard;
look it up with the `mergify_slack_channel` data source. A channel can
hold several configurations of the same product.

Every filter is **declarative** and an empty filter does not filter:
omitting all of them posts every notification of every repository.

## Example Usage

```terraform
data "mergify_slack_channel" "merge_queue" {
  owner = "Mergifyio"
  name  = "#merge-queue"
}

resource "mergify_slack_merge_queue_notification" "example" {
  owner        = "Mergifyio"
  channel_id   = data.mergify_slack_channel.merge_queue.id
  repositories = ["monorepo"]
  event_types  = ["action.queue.merged", "queue.pause.create", "queue.pause.delete"]
}
```

## Schema

### Required

- `owner` (String) GitHub organization the Slack channel is connected to.
  Changing this attribute forces resource replacement.
- `channel_id` (String) ID of the Slack channel, from the
  `mergify_slack_channel` data source. Changing this attribute forces
  resource replacement.

### Optional

- `repositories` (Set of String) Names of the organization's repositories
  to notify about. Empty or omitted means every repository.
- `event_types` (Set of String) Merge queue events posted to the channel.
  Empty or omitted means every event. The events that are posted are
  `action.queue.enter`, `action.queue.checks_start`,
  `action.queue.checks_end`, `action.queue.leave`, `action.queue.merged`,
  `queue.pause.create`, `queue.pause.update`, `queue.pause.delete`,
  `scheduled_freeze.create`, `scheduled_freeze.update` and
  `scheduled_freeze.delete`.

### Read-Only

- `id` (String) ID of the notification configuration.

## Import

An existing configuration can be imported using
`<owner>/<channel_id>/<id>`. The IDs are listed by
`GET https://api.mergify.com/v1/integrations/<owner>/configuration/slack`.

```shell
terraform import mergify_slack_merge_queue_notification.example Mergifyio/<channel_id>/<id>
```
