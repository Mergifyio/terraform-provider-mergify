---
page_title: "mergify_organization_default_products Resource - mergify"
subcategory: ""
description: |-
  Manage the default Mergify products enabled on new repositories of a GitHub organization.
---

# mergify_organization_default_products (Resource)

Manage the default set of Mergify products enabled on **new**
repositories of a GitHub organization. These defaults are applied when
Mergify discovers a repository for the first time; existing
repositories' product enablement is managed separately via
[`mergify_repository_products`](repository_products.md).

## Example Usage

```terraform
resource "mergify_organization_default_products" "example" {
  organization = "Mergifyio"
  products     = ["merge_queue", "merge_protections"]
}
```

## Schema

### Required

- `organization` (String) GitHub organization (or user) the defaults
  apply to. Changing this attribute forces resource replacement.
- `products` (Set of String) Mergify products enabled by default on new
  repositories. Known values include `merge_queue`, `merge_protections`,
  `ci_insights`, and `workflow_automation`.

### Read-Only

- `id` (String) Resource identifier — the organization name.

## Import

Existing default product enablement can be imported using the
organization name:

```shell
terraform import mergify_organization_default_products.example Mergifyio
```
