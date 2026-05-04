resource "mergify_organization_default_products" "example" {
  organization = "Mergifyio"
  products     = ["merge_queue", "merge_protections"]
}
