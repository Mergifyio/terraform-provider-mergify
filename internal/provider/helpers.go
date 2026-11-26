package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// setElements returns the elements of set, nil when it is null or unknown.
func setElements[T any](ctx context.Context, set types.Set, diags *diag.Diagnostics) []T {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	out := make([]T, 0, len(set.Elements()))
	diags.Append(set.ElementsAs(ctx, &out, false)...)
	return out
}

func setToStrings(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	return setElements[string](ctx, set, diags)
}

// setFrom returns a set of values without their duplicates, null when values
// is nil.
func setFrom[T comparable](ctx context.Context, elemType attr.Type, values []T, diags *diag.Diagnostics) types.Set {
	if values == nil {
		return types.SetNull(elemType)
	}
	set, d := types.SetValueFrom(ctx, elemType, unique(values))
	diags.Append(d...)
	return set
}

func stringSet(values []string) types.Set {
	elements := make([]attr.Value, 0, len(values))
	for _, value := range values {
		elements = append(elements, types.StringValue(value))
	}
	return types.SetValueMust(types.StringType, elements)
}

func stringMap(values map[string]string) types.Map {
	elements := make(map[string]attr.Value, len(values))
	for key, value := range values {
		elements[key] = types.StringValue(value)
	}
	return types.MapValueMust(types.StringType, elements)
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
func unique[T comparable](values []T) []T {
	out := make([]T, 0, len(values))
	seen := map[T]bool{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// sameElements tells whether a and b hold the same values, ignoring order and
// duplicates.
func sameElements[T comparable](a, b []T) bool {
	a, b = unique(a), unique(b)
	if len(a) != len(b) {
		return false
	}
	seen := map[T]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			return false
		}
	}
	return true
}
