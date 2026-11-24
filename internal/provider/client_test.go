package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
