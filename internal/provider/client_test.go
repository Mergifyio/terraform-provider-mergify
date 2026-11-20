package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// A request naming no version is served the organization's version, which
// may not be the one this release is built against.
func TestClientNamesAPIVersion(t *testing.T) {
	var (
		mu       sync.Mutex
		versions []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		versions = append(versions, r.Header.Get("Mergify-Version"))
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, "test-token", "test")

	calls := []struct {
		name string
		call func(context.Context) error
	}{
		{"GetRepositoryProducts", func(ctx context.Context) error {
			_, _, err := client.GetRepositoryProducts(ctx, "acme", "engine")
			return err
		}},
		{"SetRepositoryProducts", func(ctx context.Context) error {
			return client.SetRepositoryProducts(ctx, "acme", "engine", []string{"merge_queue"})
		}},
		{"GetDefaultProducts", func(ctx context.Context) error {
			_, err := client.GetDefaultProducts(ctx, "acme")
			return err
		}},
		{"SetDefaultProducts", func(ctx context.Context) error {
			return client.SetDefaultProducts(ctx, "acme", []string{"merge_queue"})
		}},
		{"GetSlackConfiguration", func(ctx context.Context) error {
			_, err := client.GetSlackConfiguration(ctx, "acme")
			return err
		}},
		{"CreateSlackNotification", func(ctx context.Context) error {
			_, err := client.CreateSlackNotification(ctx, "acme", "chan-ci", "ci_insights_notification", SlackNotificationBody{})
			return err
		}},
		{"UpdateSlackNotification", func(ctx context.Context) error {
			return client.UpdateSlackNotification(ctx, "acme", "chan-ci", "ci_insights_notification", "config-1", SlackNotificationBody{})
		}},
		{"DeleteSlackNotification", func(ctx context.Context) error {
			return client.DeleteSlackNotification(ctx, "acme", "chan-ci", "ci_insights_notification", "config-1")
		}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			mu.Lock()
			versions = nil
			mu.Unlock()
			if err := c.call(context.Background()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(versions) != 1 || versions[0] != "2026-11-16" {
				t.Errorf("Mergify-Version: got %q, want [\"2026-11-16\"]", versions)
			}
		})
	}
}
