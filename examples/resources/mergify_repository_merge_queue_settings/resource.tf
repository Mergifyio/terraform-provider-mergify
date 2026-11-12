resource "mergify_repository_merge_queue_settings" "example" {
  owner              = "Mergifyio"
  repository         = "monorepo"
  batch_pull_request = false
}
