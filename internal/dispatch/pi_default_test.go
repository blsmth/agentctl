package dispatch

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightPiDefault(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	tests := []struct {
		name     string
		settings string
		models   string
		wantErr  string
	}{
		{"reachable", `{"defaultProvider":"ollama"}`, `{"providers":{"ollama":{"baseUrl":"` + up.URL + `/v1"}}}`, ""},
		{"unreachable", `{"defaultProvider":"ollama"}`, `{"providers":{"ollama":{"baseUrl":"` + downURL + `/v1"}}}`, `"ollama" is unreachable at ` + downURL},
		{"missing files", "", "", ""},
		{"unparsable settings", `{nope`, `{}`, ""},
		{"no baseUrl", `{"defaultProvider":"anthropic"}`, `{"providers":{"ollama":{"baseUrl":"` + downURL + `"}}}`, ""},
		{"no models.json", `{"defaultProvider":"ollama"}`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PI_CODING_AGENT_DIR", dir)
			for name, body := range map[string]string{"settings.json": tt.settings, "models.json": tt.models} {
				if body != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := preflightPiDefault()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "--backend") {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
