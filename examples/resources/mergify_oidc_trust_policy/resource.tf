# The CI jobs of every repository get a Mergify OIDC CI token, which uploads
# their test results and CI data: no CI key to keep in a secret.
resource "mergify_oidc_trust_policy" "ci" {
  owner          = "Mergifyio"
  name           = "CI on every repository"
  github_actions = {}
}

data "github_repository" "app" {
  full_name = "Mergifyio/app"
}

data "github_repository" "workflows" {
  full_name = "Mergifyio/workflows"
}

# Or trust fewer jobs: those of app that run the release workflow of
# Mergifyio/workflows, at its protected main branch, in app's production
# environment. Policies only allow, so this one narrows nothing while the one
# above exists.
resource "mergify_oidc_trust_policy" "app_releases" {
  owner = "Mergifyio"
  name  = "app releases"

  repositories = {
    mode = "only"
    ids  = [data.github_repository.app.repo_id]
  }

  github_actions = {
    events = ["push", "workflow_dispatch"]
    workflows = [{
      repository_id = data.github_repository.workflows.repo_id
      path          = ".github/workflows/release.yml"
      ref           = "refs/heads/main"
    }]
    environments = ["production"]
  }
}
