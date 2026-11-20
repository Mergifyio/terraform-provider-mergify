---
page_title: "mergify_merge_queue_branch_settings Data Source - mergify"
subcategory: ""
description: |-
  Read the merge queue settings a base branch uses, and why.
---

# mergify_merge_queue_branch_settings (Data Source)

Read the merge queue settings a base branch uses, how each value was
settled, and every merge queue policy that applies to the branch. The
policies are managed with the `mergify_organization_merge_queue_policy`
and `mergify_repository_merge_queue_policy` resources.

Every active merge queue policy whose target holds a base branch applies
to it, with no order between them. For each setting, a branch gets the
default when no policy that applies sets it, their value when they
agree, and the safest value when they disagree: for
`batch_pull_request`, `true`, a draft pull request, which works with any
CI.

## Example Usage

```terraform
data "mergify_merge_queue_branch_settings" "main" {
  owner      = "Mergifyio"
  repository = "monorepo"
  branch     = "main"
}

output "main_batch_pull_request" {
  value = data.mergify_merge_queue_branch_settings.main.settings.batch_pull_request
}
```

## Schema

### Required

- `owner` (String) GitHub organization or user that owns the repository.
- `repository` (String) GitHub repository name.
- `branch` (String) Name of the base branch. Any branch name works,
  whether or not the branch exists.

### Read-Only

- `id` (String) Identifier in the form `<owner>/<repository>:<branch>`.
- `settings` (Attributes) What the merge queue of the base branch uses.
  See the nested schema below.
- `decisions` (Attributes) How each value of `settings` was settled. See
  the nested schema below.
- `policies` (Attributes List) Every active merge queue policy that
  applies to the base branch. See the nested schema below.

### Nested Schema for `settings`

Read-Only:

- `batch_pull_request` (Boolean) Whether the merge queue of the base
  branch opens a draft pull request to check a batch.

### Nested Schema for `decisions`

Read-Only:

- `batch_pull_request` (Attributes) How `batch_pull_request` was
  settled:
  - `rule` (String) `default` when no policy that applies sets the
    value, `policies_agree` when they all set the same value,
    `policies_disagree` when they do not and the safest value won.
  - `policy_ids` (List of String) The ids of the policies that set the
    value the branch gets; empty for `default`.

### Nested Schema for `policies`

Read-Only:

- `id` (String) The id of the policy.
- `name` (String) The policy's name.
- `repository` (String) The repository holding the policy; null for an
  organization's policy.
- `target` (Attributes) What the policy applies to: `repositories` (null
  for a repository's policy) and `branches`, each with
  `include_patterns` and `exclude_patterns` (Set of String).
- `settings` (Attributes) The merge queue settings the policy sets:
  `batch_pull_request` (Boolean), null when the policy leaves it alone.
