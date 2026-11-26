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
application key, created from the Mergify dashboard (see the
[API authentication docs][auth]).

Token resolution order, highest to lowest priority:

1. The `token` attribute on the `provider` block
2. The `MERGIFY_TOKEN` environment variable

The `GITHUB_TOKEN` environment variable is not used.

### Application key permissions

A key holds a level on each resource of the Mergify API, from No access to
Read & write (see [Application key permissions][permissions]). Grant it
what the resources and data sources you use need:

| Resource or data source | Permissions |
| --- | --- |
| `mergify_organization_default_products` | Products: Read & write |
| `mergify_repository_products` | Products: Read & write, Repositories: Read |
| `mergify_slack_ci_insights_notification` | Slack: Read & write |
| `mergify_slack_merge_queue_notification` | Slack: Read & write |
| `mergify_slack_test_quarantine_notification` | Slack: Read & write |
| `mergify_slack_channel` (data source) | Slack: Read |

A key granted **Every resource** at Read & write holds all of these, and so
does an `admin` key created before permissions existed. On a Mergify
installation that still offers only admin and CI keys, use an admin key.

A CI key, granted only CI Reporting, is refused on every resource of this
provider: when Terraform runs in a CI job, make sure `MERGIFY_TOKEN` is not
the job's CI key.

A key that lacks a permission gets `403` with the error code
`APPLICATION_KEY_REFUSED`. The API gives the same answer when `owner` is
not the key's organization or the repository does not exist, so check
those names too.

[auth]: https://docs.mergify.com/api/usage
[permissions]: https://docs.mergify.com/api/usage#application-key-permissions

## API version

Each release of the provider calls one [API version][versions] of the
Mergify API: this release calls `2026-11-20`, whatever version your
organization is on. Moving your organization to a newer version does not
move the provider. Upgrading the provider does: raise the `version` of
`Mergifyio/mergify` in `required_providers`, then run
`terraform init -upgrade`.

Before Mergify stops serving the version a release calls, the provider warns
you of the date it stops in the output of each `terraform plan` or
`terraform apply` that calls Mergify: upgrade the provider before then.

[versions]: https://docs.mergify.com/api/versions

## Schema

### Optional

- `endpoint` (String) Mergify API base URL. Defaults to
  `https://api.mergify.com/v1`. May also be set via the
  `MERGIFY_ENDPOINT` environment variable.
- `token` (String, Sensitive) Mergify API bearer token. May also be set
  via the `MERGIFY_TOKEN` environment variable.
