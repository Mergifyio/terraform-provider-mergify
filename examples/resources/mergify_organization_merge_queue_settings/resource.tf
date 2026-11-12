resource "mergify_organization_merge_queue_settings" "example" {
  owner              = "Mergifyio"
  batch_pull_request = true
}
