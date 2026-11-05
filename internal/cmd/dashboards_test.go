package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/dashboards"
)

// TestDashboardsExport verifies that cmdDashboardsExport writes one file per
// embedded dashboard into the target directory.
func TestDashboardsExport(t *testing.T) {
	dir := t.TempDir()

	// Patch os.Args so cmdDashboardsExport picks up the temp dir.
	orig := os.Args
	os.Args = []string{"agentctl", "dashboards", "export", "--dir", dir}
	defer func() { os.Args = orig }()

	cmdDashboardsExport()

	names := dashboards.List()
	for _, name := range names {
		dest := filepath.Join(dir, name)
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Errorf("exported file %q not found: %v", dest, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("exported file %q is empty", dest)
		}
		var v interface{}
		if err := json.Unmarshal(data, &v); err != nil {
			t.Errorf("exported file %q contains invalid JSON: %v", dest, err)
		}
	}
}

// TestDashboardsInstall verifies that cmdDashboardsInstall posts the correct
// Grafana API payload for each dashboard.
func TestDashboardsInstall(t *testing.T) {
	type requestRecord struct {
		path          string
		authorization string
		body          map[string]interface{}
	}

	var mu sync.Mutex
	var records []requestRecord

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)

		mu.Lock()
		records = append(records, requestRecord{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			body:          payload,
		})
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer srv.Close()

	orig := os.Args
	os.Args = []string{"agentctl", "dashboards", "install", "--url", srv.URL, "--token", "testtoken"}
	defer func() { os.Args = orig }()

	cmdDashboardsInstall()

	mu.Lock()
	defer mu.Unlock()

	names := dashboards.List()
	if len(records) != len(names) {
		t.Fatalf("expected %d requests, got %d", len(names), len(records))
	}

	for _, rec := range records {
		if rec.path != "/api/dashboards/db" {
			t.Errorf("unexpected path %q, want /api/dashboards/db", rec.path)
		}
		if !strings.HasPrefix(rec.authorization, "Bearer ") {
			t.Errorf("authorization header %q does not start with 'Bearer '", rec.authorization)
		}
		if rec.authorization != "Bearer testtoken" {
			t.Errorf("authorization = %q, want %q", rec.authorization, "Bearer testtoken")
		}
		if rec.body["overwrite"] != true {
			t.Errorf("overwrite field = %v, want true", rec.body["overwrite"])
		}
		if rec.body["dashboard"] == nil {
			t.Errorf("dashboard field is missing from payload")
		}
	}
}
