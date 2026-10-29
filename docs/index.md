---
page_title: "Mergify Provider"
description: |-
  Manage Mergify resources via the Mergify API.
---

# Mergify Provider

The Mergify provider lets you manage [Mergify](https://mergify.com)
resources declaratively via the [Mergify API](https://docs.mergify.com/api/).

## Example Usage

```terraform
terraform {
  required_providers {
    mergify = {
      source  = "Mergifyio/mergify"
      version = "~> 0.1"
    }
  }
}

provider "mergify" {
  # token reads from the MERGIFY_TOKEN environment variable
}

resource "mergify_repository_products" "monorepo" {
  owner      = "Mergifyio"
  repository = "monorepo"
  products   = ["merge_queue", "merge_protections", "ci_insights"]
}
```

## Authentication

The provider authenticates against the Mergify API with a Mergify
application key with the admin scope (created from the Mergify dashboard, see the
[API authentication docs][auth]).

Token resolution order, highest to lowest priority:

1. The `token` attribute on the `provider` block
2. The `MERGIFY_TOKEN` environment variable

The `GITHUB_TOKEN` environment variable is not used.

[auth]: https://docs.mergify.com/api/usage

## Schema

### Optional

- `endpoint` (String) Mergify API base URL. Defaults to
  `https://api.mergify.com/v1`. May also be set via the
  `MERGIFY_ENDPOINT` environment variable.
- `token` (String, Sensitive) Mergify API bearer token. May also be set
  via the `MERGIFY_TOKEN` environment variable.
