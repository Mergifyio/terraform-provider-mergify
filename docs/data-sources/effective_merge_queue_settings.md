---
page_title: "mergify_effective_merge_queue_settings Data Source - mergify"
subcategory: ""
description: |-
  Read the merge queue settings a base branch uses once its levels are resolved.
---

# mergify_effective_merge_queue_settings (Data Source)

Read the merge queue settings a base branch uses once the branch,
repository and organization levels are resolved, each with the level it
comes from. The levels are set with the
`mergify_branch_merge_queue_settings`,
`mergify_repository_merge_queue_settings` and
`mergify_organization_merge_queue_settings` resources.

## Example Usage

```terraform
data "mergify_effective_merge_queue_settings" "main" {
  owner      = "Mergifyio"
  repository = "monorepo"
  branch     = "main"
}

output "main_batch_pull_request" {
  value = data.mergify_effective_merge_queue_settings.main.batch_pull_request
}
```

## Schema

### Required

- `owner` (String) GitHub organization or user that owns the repository.
- `repository` (String) GitHub repository name.
- `branch` (String) Name of the base branch.

### Read-Only

- `id` (String) Identifier in the form `<owner>/<repository>:<branch>`.
- `batch_pull_request` (Boolean) Whether the merge queue of the base
  branch opens a draft pull request to check a batch.
- `batch_pull_request_source` (Attributes) Where `batch_pull_request`
  comes from. See the nested schema below.

### Nested Schema for `batch_pull_request_source`

Read-Only:

- `level` (String) The level the value comes from: `branch`,
  `repository`, `organization` or `default`.
- `branch` (String) The base branch name or pattern that supplied the
  value, for the `branch` level; null otherwise.
