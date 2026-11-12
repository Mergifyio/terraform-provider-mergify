---
page_title: "mergify_organization_merge_queue_policy Resource - mergify"
subcategory: ""
description: |-
  Manage a merge queue policy of an organization.
---

# mergify_organization_merge_queue_policy (Resource)

Manage a merge queue policy of an organization: the merge queue settings
it sets for the base branches of the repositories it targets. A
repository's own policies are managed with
`mergify_repository_merge_queue_policy`.

Every active merge queue policy whose target holds a base branch applies
to it, with no order between them. For each setting, a branch gets the
default when no policy that applies sets it, their value when they
agree, and the safest value when they disagree: for
`batch_pull_request`, `true`, a draft pull request, which works with any
CI. The `mergify_merge_queue_branch_settings` data source reads what a
base branch gets, and why.

Writing a policy requires the organization admin role. The merge queues
of its repositories apply a change on their next pass. Destroying the
resource deletes the policy.

## Example Usage

```terraform
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
```

## Schema

### Required

- `owner` (String) GitHub organization holding the policy. Changing this
  attribute forces resource replacement.
- `name` (String) The policy's name, unique among the policies of its
  organization or repository.
- `target` (Attributes) What the policy applies to: `repositories` and
  `branches`. See the nested schemas below.
- `settings` (Attributes) The merge queue settings the policy sets, at
  least one. See the nested schema below.

### Optional

- `enforcement` (String) `active` applies the policy; `disabled` keeps
  it listed without applying it. Defaults to `active`.

### Read-Only

- `id` (String) The id of the policy.

### Nested Schema for `target`

Required:

- `repositories` (Attributes) The repositories the policy applies to.
  See below.
- `branches` (Attributes) The base branches the policy applies to. See
  below.

### Nested Schema for `target.repositories`

Required:

- `include_patterns` (Set of String) The repositories targeted, at least
  one. A name, or a glob pattern matching names as the `*=` condition
  operator does: `*` matches within one path segment, `**` across `/`.
  `~ALL` matches every one.

Optional:

- `exclude_patterns` (Set of String) The repositories never targeted,
  even when included. A name, or a glob pattern matching names as the
  `*=` condition operator does: `*` matches within one path segment,
  `**` across `/`. `~ALL` matches every one. Defaults to none.

### Nested Schema for `target.branches`

Required:

- `include_patterns` (Set of String) The base branches targeted, at
  least one. A name, or a glob pattern matching names as the `*=`
  condition operator does: `*` matches within one path segment, `**`
  across `/`. `~ALL` matches every one. `~DEFAULT_BRANCH` matches the
  repository's default branch.

Optional:

- `exclude_patterns` (Set of String) The base branches never targeted,
  even when included. A name, or a glob pattern matching names as the
  `*=` condition operator does: `*` matches within one path segment,
  `**` across `/`. `~ALL` matches every one. `~DEFAULT_BRANCH` matches
  the repository's default branch. Defaults to none.

### Nested Schema for `settings`

Optional:

- `batch_pull_request` (Boolean) Whether the merge queue opens a draft
  pull request to check a batch. When `false`, Mergify pushes the batch
  to its queue branch and reads the CI of that branch's head commit,
  with no pull request: the CI of the base branch must run on a `push`
  to the branches under the `queue_branch_prefix` of your queue rules
  (`mergify/merge-queue/` by default). A queue rule that uses
  `merge_method: fast-forward` or `allow_queue_branch_edit`, or
  `merge_method: merge-batch` in the `isolated` mode or with a
  `merge_bot_account` in the `parallel` mode, needs a draft pull
  request: unless its checks run in place, Mergify does not queue a pull
  request for such a rule on a base branch where this is `false`. Left
  out, the policy does not set it.

  `false` is in preview and available on request: until Mergify Support
  enables it for your organization, the apply fails with
  `batch_pull_request = false is not available to this organization`.

A pattern the API refuses fails the apply with `Invalid merge queue
policy target pattern`, a name another policy of the same organization
or repository holds with `Merge queue policy name already taken`. An
organization, and a repository, hold at most 100 policies: past that,
the apply fails with `Too many merge queue policies`.

## Import

A merge queue policy can be imported using `<owner>:<policy_id>`:

```shell
terraform import mergify_organization_merge_queue_policy.no_drafts Mergifyio:0b7e4c1a-1f0e-4a8e-9c52-5d1c1d0a2b3c
```
