# terraform-provider-mergify

The README's "Development" section has the commands: `make build`,
`make test`, `make install`.

## The API version a release calls

`apiVersion` in `internal/provider/client.go` is the Mergify API version
every request of the provider names in its `Mergify-Version` header. A
release calls the version it was built and tested against, whatever version
the user's organization is on.

A released API version keeps every operation and field it serves, and
gains the ones Mergify adds to the API; a removal or a breaking change comes
only with a newer version
([API Versions](https://docs.mergify.com/api/versions)). So:

- Calling what the pinned version serves, an operation added since
  included, needs no change to the pin.
- Move the pin when the provider needs what only a newer version serves, or
  before the pinned one stops being served: Mergify's answers then carry
  that date in a `Mergify-Version-Sunset` header. Each version's page under
  [API Versions](https://docs.mergify.com/api/versions) says what it
  changes: adapt every call it changes in the same pull request.
- Never pin `next`: it is not released, and changes until it is.
- A moved pin reaches users only with a release: a `v*` tag pushed on
  `main` runs `.github/workflows/release.yml`, which builds and publishes
  it.
