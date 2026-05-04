# Terraform Provider for Mergify

Manage [Mergify](https://mergify.com) resources via Terraform.

## Status

Early development. Currently exposes:

- `mergify_repository_products` — manage which Mergify products are enabled on a GitHub repository.

## Usage

```hcl
terraform {
  required_providers {
    mergify = {
      source  = "Mergifyio/mergify"
      version = "~> 0.1"
    }
  }
}

provider "mergify" {
  # token = "..."           # or set MERGIFY_TOKEN
  # endpoint = "https://api.mergify.com/v1"  # or set MERGIFY_ENDPOINT
}

resource "mergify_repository_products" "monorepo" {
  owner      = "Mergifyio"
  repository = "monorepo"
  products   = ["merge_queue", "merge_protections", "ci_insights"]
}
```

## Authentication

Provide a Mergify application key or a GitHub personal access token (see
[Mergify API auth][auth]) via, in priority order:

1. The `token` provider attribute
2. The `MERGIFY_TOKEN` environment variable
3. The `GITHUB_TOKEN` environment variable

[auth]: https://docs.mergify.com/api/usage

## Development

```bash
make tidy   # populate go.sum
make build  # build the provider
make test   # run tests
```

To use the locally-built provider, run `make install` and configure a
[dev_overrides][devo] block in `~/.terraformrc`.

[devo]: https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers

## License

Mozilla Public License 2.0. See [LICENSE](LICENSE).
