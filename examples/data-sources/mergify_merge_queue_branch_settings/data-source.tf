data "mergify_merge_queue_branch_settings" "main" {
  owner      = "Mergifyio"
  repository = "monorepo"
  branch     = "main"
}

output "main_batch_pull_request" {
  value = data.mergify_merge_queue_branch_settings.main.settings.batch_pull_request
}
