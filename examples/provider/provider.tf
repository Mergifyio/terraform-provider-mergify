terraform {
  required_providers {
    mergify = {
      source  = "Mergifyio/mergify"
      version = "~> 0.1"
    }
  }
}

provider "mergify" {
  # token reads from MERGIFY_TOKEN env var by default
  # endpoint reads from MERGIFY_ENDPOINT (defaults to https://api.mergify.com/v1)
}
