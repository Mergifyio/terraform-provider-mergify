# Every release branch, such as release/1.0, checks its batches without a
# draft pull request.
resource "mergify_branch_merge_queue_settings" "release" {
  owner              = "Mergifyio"
  repository         = "monorepo"
  branch             = "release/*"
  batch_pull_request = false
}
