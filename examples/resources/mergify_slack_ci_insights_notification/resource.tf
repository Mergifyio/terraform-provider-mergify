data "mergify_slack_channel" "ci" {
  owner = "Mergifyio"
  name  = "#ci"
}

resource "mergify_slack_ci_insights_notification" "example" {
  owner       = "Mergifyio"
  channel_id  = data.mergify_slack_channel.ci.id
  branches    = ["main"]
  conclusions = ["failure", "timed_out"]
}
