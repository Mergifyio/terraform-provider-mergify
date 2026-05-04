---
page_title: "mergify_repository_products Resource - mergify"
subcategory: ""
description: |-
  Manage which Mergify products are enabled on a GitHub repository.
---

# mergify_repository_products (Resource)

Manage which Mergify products are enabled on a GitHub repository.

The `products` attribute is **declarative**: applying the resource sets
the exact set of enabled products on the repository, removing any that
are not listed.

## Example Usage

```terraform
resource "mergify_repository_products" "example" {
  owner      = "Mergifyio"
  repository = "monorepo"
  products   = ["merge_queue", "merge_protections", "ci_insights"]
}
```

## Schema

### Required

- `owner` (String) GitHub organization or user that owns the repository.
  Changing this attribute forces resource replacement.
- `repository` (String) GitHub repository name. Changing this attribute
  forces resource replacement.
- `products` (Set of String) Mergify products to enable on the
  repository. Known values include `merge_queue`, `merge_protections`,
  `ci_insights`, and `workflow_automation`.

### Read-Only

- `id` (String) Resource identifier in the form `<owner>/<repository>`.

## Import

Existing repository product enablement can be imported using
`<owner>/<repository>`:

```shell
terraform import mergify_repository_products.example Mergifyio/monorepo
```
