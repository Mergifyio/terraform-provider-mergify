package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The steps every message asking to upgrade the provider gives.
const upgradeSteps = "raise the version of Mergifyio/mergify in required_providers, then run `terraform init -upgrade`."

// A request naming no version is served the organization's version, which
// may not be the one this release is built against.
func TestClientNamesAPIVersion(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("Mergify-Version"):
		default:
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, "test-token", "test")

	if _, err := client.GetDefaultProducts(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if version := <-got; version != "2026-11-20" {
		t.Errorf("Mergify-Version: got %q, want %q", version, "2026-11-20")
	}
}

// A server that does not serve the version a release calls refuses every
// request, and only the versions it serves tell which side has to move.
func TestClientNamesTheWayOutOfAVersionRefusal(t *testing.T) {
	refusal := "This release of terraform-provider-mergify calls Mergify's API as version " + apiVersion +
		", which this Mergify server does not serve (it serves %s). %s"
	for _, tc := range []struct {
		name   string
		served []string
		want   string
	}{
		{
			name:   "retired",
			served: []string{"2099-06-01", "2099-01-01"},
			want:   fmt.Sprintf(refusal, "2099-01-01, 2099-06-01", "Upgrade terraform-provider-mergify: "+upgradeSteps),
		},
		{
			name:   "retired, an older one kept",
			served: []string{"2000-01-01", "2099-01-01", "not a date"},
			want:   fmt.Sprintf(refusal, "2000-01-01, 2099-01-01", "Upgrade terraform-provider-mergify: "+upgradeSteps),
		},
		{
			name:   "older server",
			served: []string{"2000-01-01"},
			want: fmt.Sprintf(refusal, "2000-01-01",
				"Upgrade Mergify to a release that serves "+apiVersion+", or use an older release of terraform-provider-mergify: "+
					"lower the version of Mergifyio/mergify in required_providers, then run `terraform init -upgrade`."),
		},
		{
			name: "unlisted",
			want: "This release of terraform-provider-mergify calls Mergify's API as version " + apiVersion +
				", which this Mergify server does not serve. Upgrade Mergify, or upgrade terraform-provider-mergify: " + upgradeSteps,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"detail":          "This server does not serve the API version the Mergify-Version header names.",
					"error_code":      "API_VERSION_UNKNOWN",
					"served_versions": tc.served,
				})
			}))
			t.Cleanup(srv.Close)

			_, err := NewClient(srv.URL, "test-token", "test").GetDefaultProducts(context.Background(), "acme")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error:\n got %v\nwant %s", err, tc.want)
			}
			if code, _ := APIErrorCode(err); code != "API_VERSION_UNKNOWN" {
				t.Errorf("APIErrorCode: got %q", code)
			}
		})
	}
}

// Any other refusal is the API's own error, body included.
func TestClientKeepsOtherBadRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusBadRequest, "SOMETHING_ELSE", "no")
	}))
	t.Cleanup(srv.Close)

	_, err := NewClient(srv.URL, "test-token", "test").GetDefaultProducts(context.Background(), "acme")
	if apiErr, ok := err.(*APIError); !ok || !strings.Contains(apiErr.Body, "SOMETHING_ELSE") {
		t.Fatalf("error: got %#v, want the API's error", err)
	}
}
