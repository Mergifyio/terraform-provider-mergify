resource "mergify_repository_products" "example" {
  owner      = "Mergifyio"
  repository = "monorepo"
  products   = ["merge_queue", "merge_protections", "ci_insights"]
}
