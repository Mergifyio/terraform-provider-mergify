# The default branch and the release branches of every repository but docs
# check their batches without a draft pull request.
resource "mergify_organization_merge_queue_policy" "no_drafts" {
  owner = "Mergifyio"
  name  = "Batches without draft pull requests"

  target = {
    repositories = {
      include_patterns = ["~ALL"]
      exclude_patterns = ["docs"]
    }
    branches = {
      include_patterns = ["~DEFAULT_BRANCH", "release/*"]
    }
  }

  settings = {
    batch_pull_request = false
  }
}
