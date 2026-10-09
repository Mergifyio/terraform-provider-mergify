package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func setToStrings(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(set.Elements()))
	diags.Append(set.ElementsAs(ctx, &out, false)...)
	return out
}

// orEmpty turns a nil slice into an empty one, which encodes as `[]` rather
// than `null` in JSON and in Terraform sets.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// unique returns values without duplicates, never nil: the API stores lists
// and accepts duplicates, which a Terraform set refuses.
func unique(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
