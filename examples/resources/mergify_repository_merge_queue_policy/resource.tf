resource "mergify_repository_merge_queue_policy" "main" {
  owner      = "Mergifyio"
  repository = "monorepo"
  name       = "main without draft pull requests"

  target = {
    branches = {
      include_patterns = ["main"]
    }
  }

  settings = {
    batch_pull_request = false
  }
}
