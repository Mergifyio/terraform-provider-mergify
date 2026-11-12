---
page_title: "mergify_repository_merge_queue_settings Resource - mergify"
subcategory: ""
description: |-
  Manage the merge queue settings a repository sets for its base branches.
---

# mergify_repository_merge_queue_settings (Resource)

Manage the merge queue settings a repository sets for its base
branches. A base branch (`mergify_branch_merge_queue_settings`) may
override each value, and a value the repository leaves unset comes from
its organization (`mergify_organization_merge_queue_settings`).

A base branch's value comes from the narrowest level setting it: the
branch, then the repository, then the organization, then the default.
The `mergify_effective_merge_queue_settings` data source reads what a
base branch ends up with.

Writing the settings requires the admin role on the repository.
Destroying the resource unsets every merge queue setting of the
repository, which its base branches then take from the organization.

## Example Usage

```terraform
resource "mergify_repository_merge_queue_settings" "example" {
  owner              = "Mergifyio"
  repository         = "monorepo"
  batch_pull_request = false
}
```

## Schema

### Required

- `owner` (String) GitHub organization or user that owns the repository.
  Changing this attribute forces resource replacement.
- `repository` (String) GitHub repository name. Changing this attribute
  forces resource replacement.

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
  leaves it to the organization.

  `false` is in preview and available on request: until Mergify Support
  enables it for your organization, the apply fails with
  `batch_pull_request = false is not available to this organization`.

### Read-Only

- `id` (String) Resource identifier in the form `<owner>/<repository>`.

## Import

The merge queue settings of a repository can be imported using
`<owner>/<repository>`:

```shell
terraform import mergify_repository_merge_queue_settings.example Mergifyio/monorepo
```
