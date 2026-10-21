---
page_title: "mergify_pipeline Resource - mergify"
subcategory: ""
description: |-
  Define a Job Engine pipeline of a GitHub repository.
---

# mergify_pipeline (Resource)

Define a Job Engine pipeline of a GitHub repository: what triggers it, the
initial job each of its runs starts with, and the Vault its jobs read their
secrets from. A repository can have several pipelines, `ci` and `deploy`
say, each run on its own and reported by its own check,
`Mergify Job Engine / <name>`.

Creating, changing or deleting a pipeline requires Mergify's admin role on
the repository. Every change is recorded in the repository's activity log.

The resource is **declarative**: applying it replaces the whole definition,
so an optional attribute left out takes its default, whatever the
dashboard set. Changing any attribute but `owner` and `repository` updates
the pipeline in place, renaming it included.

## Example Usage

```terraform
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
```

## Schema

### Required

- `owner` (String) GitHub organization or user that owns the repository.
  Changing this attribute, other than its case, forces resource
  replacement.
- `repository` (String) GitHub repository name. Changing this attribute,
  other than its case, forces resource replacement.
- `name` (String) Name of the pipeline, unique in its repository whatever
  the case. Each run of the pipeline posts the check
  `Mergify Job Engine / <name>`: renaming a pipeline renames its check,
  which breaks a branch protection or queue condition that requires it.
- `initial_job` (Attributes) The first job of each run, which writes the
  job list of the rest. See the nested schema below.

### Optional

- `triggers` (Attributes Set) What runs the pipeline, one trigger per
  context. Omitted: a pull request or a merge queue batch, whatever its
  branch or queue. Empty: nothing runs it. See the nested schema below.
- `vault` (Attributes) Where the jobs of the pipeline's runs read their
  secrets. Omitted: they read none. Required when `initial_job` declares
  `secrets`. See the nested schema below.

### Read-Only

- `id` (String) ID of the pipeline.

### Nested Schema for `triggers`

Required:

- `context` (String) `pull_request`: a pull request is opened, reopened or
  pushed to. `merge_queue`: the merge queue checks a batch of pull
  requests.

Optional:

- `branches` (Set of String) `pull_request` only: run only for a pull
  request whose target branch matches one of these patterns, where `*`
  matches any run of characters, `/` included. Omitted: whatever its
  target branch. Not empty.
- `queues` (Set of String) `merge_queue` only: run only for the batches of
  these queues. Omitted: every queue. Not empty.

### Nested Schema for `initial_job`

Required:

- `image` (String) Docker image the initial job runs on the run's head
  commit.

Optional:

- `args` (List of String) Replace the image's CMD and keep its ENTRYPOINT,
  as `docker run <image> <args>` does. Omitted: the image's CMD. Not
  empty. They appear in the run's logs: no secret here.
- `runs_on` (String) Runner label the initial job runs on. Omitted: the
  default runner of the repository's pipeline workflow.
- `secrets` (Attributes Map) Secrets the initial job reads from `vault`,
  keyed by the name it receives each under: the file
  `/mergify-pipeline/secrets/<name>`, or the environment variable of that
  name. Not empty. The jobs of the run declare their own in the job list.
  See the nested schema below.

### Nested Schema for `initial_job.secrets`

Required:

- `path` (String) Vault path to read, as Vault's HTTP API names it:
  `secret/data/ci/registry` for a KV version 2 secret.
- `field` (String) Field of the secret's data to hand over.

Optional:

- `role` (String) Vault role to log in with. Omitted: `vault.default_role`,
  which is then required.
- `env` (Boolean) Hand the secret over as an environment variable rather
  than as a file. Anyone who can inspect the container on its runner can
  read its environment.

### Nested Schema for `vault`

None of it is a credential: the Vault roles' bound claims decide on the
token Mergify signs for each job.

Required:

- `address` (String) `https://` URL of the Vault server.
- `audience` (String) Audience of the tokens Mergify signs for the
  pipeline's jobs, which the Vault roles' `bound_audiences` must name.

Optional:

- `auth_mount` (String) Path of Vault's JWT auth method. Omitted: `jwt`.
- `namespace` (String) Vault Enterprise namespace.
- `ca_certificate` (String) PEM certificates the Vault server's
  certificate is checked against. Omitted: the runner's trusted ones.
  Mergify stores the certificates alone and drops a private key pasted
  along; the state keeps the text as configured.
- `default_role` (String) Vault role a secret that names none logs in
  with.

## Import

An existing pipeline can be imported using `<owner>/<repository>/<id>`.
The IDs are listed by
`GET https://api.mergify.com/v1/repos/<owner>/<repository>/pipelines`.

```shell
terraform import mergify_pipeline.ci Mergifyio/monorepo/<id>
```
