---
page_title: "mergify_branch_merge_queue_settings Resource - mergify"
subcategory: ""
description: |-
  Manage the merge queue settings a base branch name or pattern sets in a repository.
---

# mergify_branch_merge_queue_settings (Resource)

Manage the merge queue settings a base branch name or pattern sets in a
repository. One resource with a pattern such as `release/*` covers every
branch it matches, including the branches that do not exist yet.

A base branch's value comes from the narrowest level setting it: the
branch, then the repository (`mergify_repository_merge_queue_settings`),
then the organization (`mergify_organization_merge_queue_settings`),
then the default. At the branch level, an entry naming the branch
exactly comes before every pattern; between patterns, the one with the
longest literal prefix (what comes before its first wildcard) comes
first, then the longest pattern, then alphabetical order. The
`mergify_effective_merge_queue_settings` data source reads what a base
branch ends up with.

Writing the settings requires the admin role on the repository.
Destroying the resource unsets every merge queue setting of the branch
name or pattern: the branches it named take them from the next pattern
matching them, then from the repository.

## Example Usage

```terraform
# Every release branch, such as release/1.0, checks its batches without a
# draft pull request.
resource "mergify_branch_merge_queue_settings" "release" {
  owner              = "Mergifyio"
  repository         = "monorepo"
  branch             = "release/*"
  batch_pull_request = false
}
```

## Schema

### Required

- `owner` (String) GitHub organization or user that owns the repository.
  Changing this attribute forces resource replacement.
- `repository` (String) GitHub repository name. Changing this attribute
  forces resource replacement.
- `branch` (String) Base branch name, or a glob pattern matching base
  branch names as the `*=` condition operator does (e.g. `release/*`,
  `stable/**`). A literal `{`, `}` or `,` in a branch name is escaped
  with `\`. An invalid pattern fails the apply with `Invalid base branch
  pattern`. Changing this attribute forces resource replacement.

### Optional

- `batch_pull_request` (Boolean) Whether the merge queue opens a draft
  pull request to check a batch. When `false`, Mergify pushes the batch
  to its queue branch and reads the CI of that branch's head commit,
  with no pull request: the CI of the base branch must run on a `push`
  to the branches under the `queue_branch_prefix` of your queue rules
  (`mergify/merge-queue/` by default). A queue rule that needs a draft
  pull request, such as one using `merge_method: fast-forward` or
  `allow_queue_branch_edit`, does not queue a pull request on a base
  branch where this is `false`, unless its checks run in place. Unset
  leaves it to the next pattern matching the branch, then to the
  repository.

  `false` is in preview and available on request: until Mergify Support
  enables it for your organization, the apply fails with
  `batch_pull_request = false is not available to this organization`.

### Read-Only

- `id` (String) Resource identifier in the form
  `<owner>/<repository>:<branch>`.

## Import

The merge queue settings of a base branch name or pattern can be
imported using `<owner>/<repository>:<branch>`. The branch comes after
the first `:`, which a branch name cannot hold, so it may contain `/`:

```shell
terraform import mergify_branch_merge_queue_settings.release 'Mergifyio/monorepo:release/*'
```
