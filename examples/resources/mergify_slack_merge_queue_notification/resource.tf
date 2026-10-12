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
