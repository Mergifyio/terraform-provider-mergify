resource "mergify_pipeline" "ci" {
  owner      = "Mergifyio"
  repository = "monorepo"
  name       = "ci"

  # Pull requests targeting main or a release branch, and every merge queue
  # batch. Omitted, any pull request and any batch run the pipeline.
  triggers = [
    { context = "pull_request", branches = ["main", "release/*"] },
    { context = "merge_queue" },
  ]

  initial_job = {
    image = "ghcr.io/mergifyio/ci-jobs:1"
    args  = ["generate"]
    secrets = {
      REGISTRY_TOKEN = { path = "secret/data/ci/registry", field = "token" }
    }
  }

  vault = {
    address      = "https://vault.example.com"
    audience     = "mergify"
    default_role = "ci"
  }
}
