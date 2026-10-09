data "mergify_slack_channel" "ci" {
  owner = "Mergifyio"
  name  = "#ci"
}

resource "mergify_slack_test_quarantine_notification" "example" {
  owner        = "Mergifyio"
  channel_id   = data.mergify_slack_channel.ci.id
  repositories = ["monorepo"]
  event_types  = ["test.quarantined"]
}
