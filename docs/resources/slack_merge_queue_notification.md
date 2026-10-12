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
  events = [
    # Only the pull requests that left the queue without being merged.
    { type = "action.queue.leave", outcomes = ["failure"] },
    { type = "queue.pause.create" },
    { type = "queue.pause.delete" },
  ]
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
- `events` (Attributes Set) Merge queue events posted to the channel, each
  with the outcomes it posts. Empty or omitted means every event. The
  events that are posted are `action.queue.enter`,
  `action.queue.checks_start`, `action.queue.checks_end`,
  `action.queue.leave`, `action.queue.merged`, `queue.pause.create`,
  `queue.pause.update`, `queue.pause.delete`, `scheduled_freeze.create`,
  `scheduled_freeze.update` and `scheduled_freeze.delete`. See the nested
  schema below.

### Read-Only

- `id` (String) ID of the notification configuration.

### Nested Schema for `events`

Required:

- `type` (String) Merge queue event.

Optional:

- `outcomes` (Set of String) Outcomes of the event that are posted. Empty
  or omitted means every outcome. Two events have several:
  - `action.queue.leave`: `success` when the queue merged the pull
    request, `neutral` when it was merged outside the queue, `failure`
    when it left the queue without being merged.
  - `action.queue.checks_end`: `failure` when the checks failed,
    `pending` when they passed and the pull request waits to be merged.

  Every other event has one: `pending` for `action.queue.enter` and
  `action.queue.checks_start`, `success` for `action.queue.merged`, and
  `neutral` for the pause and freeze events.

## Import

An existing configuration can be imported using
`<owner>/<channel_id>/<id>`. The IDs are listed by
`GET https://api.mergify.com/v1/integrations/<owner>/configuration/slack`.

```shell
terraform import mergify_slack_merge_queue_notification.example Mergifyio/<channel_id>/<id>
```
