package provider

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// Every answer served at a version Mergify retires dates its end; a provider
// process warns of it once.
func TestClientWarnsOnceOfTheEndOfItsAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sunsets []string
		want    diag.Diagnostics
	}{
		{
			name:    "dated",
			sunsets: []string{"", "not a date", "2099-06-01", "2099-06-01"},
			want: diag.Diagnostics{diag.NewWarningDiagnostic(
				"Mergify stops serving API version "+apiVersion+" on 2099-06-01",
				"This release of terraform-provider-mergify calls Mergify's API as version "+apiVersion+
					", which Mergify stops serving on 2099-06-01 (UTC). Upgrade terraform-provider-mergify, whose newer releases call a newer version: "+
					upgradeSteps,
			)},
		},
		{name: "undated", sunsets: []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := make(chan string, len(tc.sunsets))
			for _, sunset := range tc.sunsets {
				answers <- sunset
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case sunset := <-answers:
					if sunset != "" {
						w.Header().Set("Mergify-Version-Sunset", sunset)
					}
					writeJSON(w, http.StatusOK, map[string]any{})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(srv.Close)
			client := NewClient(srv.URL, "test-token", "test")

			var diags diag.Diagnostics
			for range tc.sunsets {
				if _, err := client.GetDefaultProducts(context.Background(), "acme"); err != nil {
					t.Fatal(err)
				}
				client.warnOfSunset(&diags)
			}
			if !diags.Equal(tc.want) {
				t.Errorf("diagnostics:\n got %v\nwant %v", diags, tc.want)
			}
		})
	}
}

// The warning reaches a run through whichever operation reads the date
// first, so every operation that calls Mergify defers it.
func TestEveryOperationCallingMergifyWarnsOfTheEndOfItsAPIVersion(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || !isOperation(fn.Name.Name) || !uses(fn.Body, "client") {
				continue
			}
			checked++
			if !defersWarning(fn.Body) {
				t.Errorf("%s: %s calls Mergify without `defer ….client.warnOfSunset(&resp.Diagnostics)`", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no operation calling Mergify")
	}
}

func isOperation(name string) bool {
	switch name {
	case "Create", "Read", "Update", "Delete":
		return true
	}
	return false
}

func uses(node ast.Node, field string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
			found = true
		}
		return !found
	})
	return found
}

func defersWarning(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		if d, ok := stmt.(*ast.DeferStmt); ok {
			if sel, ok := d.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "warnOfSunset" {
				return true
			}
		}
	}
	return false
}
