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
