package artifacturl

import (
	"errors"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestDirect(t *testing.T) {
	tests := []struct {
		base, id string
		rev      int
		want     string
	}{
		{"https://mac.local:8443", "abc", 0, "https://mac.local:8443/a/abc/latest/"},
		{"https://mac.local:8443/", "abc", 3, "https://mac.local:8443/a/abc/3/"},
	}
	for _, tt := range tests {
		if got := Direct(tt.base, tt.id, tt.rev); got != tt.want {
			t.Errorf("Direct(%q, %q, %d) = %q, want %q", tt.base, tt.id, tt.rev, got, tt.want)
		}
	}
}

func TestChooseBase(t *testing.T) {
	const local = "https://mac.local:8443"
	const public = "https://mac.example.com:8443/"
	tests := []struct {
		name      string
		hostname  string
		running   bool
		publicURL string
		queryErr  error
		want      string
		wantQuery bool
	}{
		{name: "unset never queries", running: true, publicURL: public, want: local},
		{name: "not running", hostname: "mac.example.com", publicURL: public, want: local},
		{name: "no valid cert yet", hostname: "mac.example.com", running: true, want: local, wantQuery: true},
		{name: "query error", hostname: "mac.example.com", running: true, publicURL: public, queryErr: errors.New("x"), want: local, wantQuery: true},
		{name: "valid cert", hostname: "mac.example.com", running: true, publicURL: public, want: "https://mac.example.com:8443", wantQuery: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queried := false
			got := chooseBase(config.ServeConfig{PublicHostname: tt.hostname}, "mac", tt.running, func() (string, error) {
				queried = true
				return tt.publicURL, tt.queryErr
			})
			if got != tt.want {
				t.Errorf("base = %q, want %q", got, tt.want)
			}
			if queried != tt.wantQuery {
				t.Errorf("queried = %v, want %v", queried, tt.wantQuery)
			}
		})
	}
}
