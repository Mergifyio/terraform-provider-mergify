data "mergify_effective_merge_queue_settings" "main" {
  owner      = "Mergifyio"
  repository = "monorepo"
  branch     = "main"
}

output "main_batch_pull_request" {
  value = data.mergify_effective_merge_queue_settings.main.batch_pull_request
}
