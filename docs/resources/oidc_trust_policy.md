---
page_title: "mergify_oidc_trust_policy Resource - mergify"
subcategory: ""
description: |-
  Manage an OIDC trust policy of an organization.
---

# mergify_oidc_trust_policy (Resource)

Manage an OIDC trust policy of an organization: which GitHub Actions
jobs get a Mergify OIDC CI token from their ID token, and what that
token may do on the job's repository. [OIDC Authentication for GitHub
Actions][oidc] says how a job exchanges its ID token.

A job gets a token when one policy of its organization matches it, and
a policy matches when each of its lines does: `repositories`, and the
`events`, `workflows` and `environments` of `github_actions`. Policies
only allow: there is no order and no deny, so a strict policy cannot
hold back what a broad one allows.

A change applies to the next exchange: the tokens already issued keep
what the policy granted until they expire, within the hour. Destroying
the resource deletes the policy, and the jobs only it allowed get no
token from then on.

## Application key permissions

The application key needs OIDC Trust Policies at Read & write
(`oidc_trust_policies: write` in the [API Reference][api-reference]).

An application key writes, changes and deletes only the policies that
grant CI Reporting alone, which is what `permissions` defaults to. A
policy granting more is written by an organization admin, signed in as
themselves, in the Mergify dashboard or with the `mergify` CLI.
Terraform still reads what a policy grants: a grant added outside
Terraform shows in the plan, and applying it fails with `Trust policy
permissions refused`.

## Example Usage

```terraform
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
```

## Schema

### Required

- `owner` (String) GitHub organization holding the policy. Changing this
  attribute forces resource replacement.
- `name` (String) The policy's name, unique among the organization's
  trust policies.
- `github_actions` (Attributes) What the policy matches of a GitHub
  Actions job, besides its repository: its events, workflows and
  environments. `{}` matches the jobs started by the CI events, running
  any workflow, in any environment. See the nested schema below.

### Optional

- `repositories` (Attributes) The repositories whose jobs the policy
  matches. Defaults to every repository Mergify's GitHub App can access,
  new ones included. See the nested schema below.
- `permissions` (Map of String) What the token of a job the policy
  matches may do on the job's own repository, resource by resource, as
  an application key's permissions: `read`, or `write`, which includes
  read. `*` gives its level on every resource but `ci_reporting`. A job
  several policies match gets what each of them grants. Defaults to
  `{ ci_reporting = "write" }`, CI Reporting alone: an application key
  writes no other (see [Application key
  permissions](#application-key-permissions)).

### Read-Only

- `id` (String) The id of the policy.

### Nested Schema for `repositories`

Required:

- `mode` (String) `all`: every repository Mergify's GitHub App can
  access, new ones included as they are added. `only` the repositories
  of `ids`, or `all_except` them, new ones included. `visibility`: every
  repository whose visibility is one of `visibilities`, new ones
  included.

Optional:

- `ids` (Set of Number) The GitHub ids of the repositories, for the
  `only` and `all_except` modes: the `repo_id` of the GitHub provider's
  `github_repository`.
- `visibilities` (Set of String) `public`, `internal` or `private`, for
  the `visibility` mode: matched against the visibility the job's ID
  token gives its repository when the job runs. A job whose ID token
  gives none is not matched.

### Nested Schema for `github_actions`

Optional:

- `events` (Set of String) The job's `event_name`, or `["any"]` for any
  event. Defaults to the CI events: `push`, `pull_request`,
  `merge_group`, `schedule` and `workflow_dispatch`.
  `pull_request_target`, `workflow_run` and `issue_comment` run with the
  repository's identity on behalf of people who cannot push to it: they
  stay out unless listed.
- `workflows` (Attributes Set) The workflow the job runs. Defaults to
  any. See the nested schema below.
- `environments` (Set of String) The job's environment. Defaults to any,
  which includes a job that runs in none.

### Nested Schema for `github_actions.workflows`

Required:

- `path` (String) The workflow file's path in its repository:
  `.github/workflows/ci.yml`.

Optional:

- `repository_id` (Number) For a reusable workflow the job runs, the
  GitHub id of the repository holding it, one of the organization's
  that Mergify's GitHub App can access. Left out, the workflow file is
  one of the job's own repository, which started the run.
- `ref` (String) For a reusable workflow, the ref of it that matches: a
  ref (`refs/heads/main`, `refs/tags/v1`) or a commit sha. Left out, any
  ref matches. A protected ref is the one way to be sure that someone
  pushing a branch cannot edit the workflow that holds the token.

A name another policy of the organization holds fails the apply with
`Trust policy name already taken`, a repository id Mergify's GitHub App
cannot access in the organization with `Unknown repository in trust
policy`. An organization holds at most 100 policies: past that, the
apply fails with `Too many trust policies`. On a Mergify that predates
trust policy permissions, whose policies grant CI Reporting alone,
`permissions` other than the default fails with `Permissions unknown to
this Mergify`.

## Import

A trust policy can be imported using `<owner>:<trust_policy_id>`, the
id `mergify oidc trust-policies list` shows:

```shell
terraform import mergify_oidc_trust_policy.ci Mergifyio:0b7e4c1a-1f0e-4a8e-9c52-5d1c1d0a2b3c
```

[oidc]: https://docs.mergify.com/integrations/gha/oidc
[api-reference]: https://docs.mergify.com/api
