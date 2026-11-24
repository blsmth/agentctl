package auditnarrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedact(t *testing.T) {
	old := homeDir
	homeDir = func() (string, error) { return "/Users/bob", nil }
	t.Cleanup(func() { homeDir = old })

	tests := []struct {
		name, in, want string
		gone           []string // must not survive
	}{
		{"env assign", "API_TOKEN=abc123xyz curl x", "API_TOKEN=[REDACTED] curl x", []string{"abc123xyz"}},
		{"password quoted", `DB_PASSWORD="p@ss word" ./run`, `DB_PASSWORD=[REDACTED] ./run`, []string{"p@ss"}},
		{"aws secret", "AWS_SECRET_ACCESS_KEY=wJalr/abc ls", "AWS_SECRET_ACCESS_KEY=[REDACTED] ls", []string{"wJalr"}},
		{"flag equals", "tool --api-key=zzz999", "tool --api-key=[REDACTED]", []string{"zzz999"}},
		{"flag space", "mysql --password hunter2 db", "mysql --password [REDACTED] db", []string{"hunter2"}},
		{"json", `{"auth": "s3cr3tvalue"}`, `{"auth": [REDACTED]}`, []string{"s3cr3tvalue"}},
		{"auth header", `curl -H "Authorization: Bearer abcdef123456" x`, `curl -H "Authorization: [REDACTED]" x`, []string{"abcdef123456"}},
		{"bearer alone", "echo Bearer abcdefgh12345", "echo Bearer [REDACTED]", []string{"abcdefgh12345"}},
		{"openai", "echo sk-abcdEFGH1234", "echo [REDACTED]", []string{"sk-abcd"}},
		{"github", "git clone https://x/ghp_abcdefghij123456", "git clone https://x/[REDACTED]", []string{"ghp_abc"}},
		{"github pat", "github_pat_11ABCDEFG0abcdefgh_xyz", "[REDACTED]", []string{"11ABCDEFG"}},
		{"slack", "xoxb-1234-abcdefgh", "[REDACTED]", []string{"1234-abcdefgh"}},
		{"aws key id", "id AKIAIOSFODNN7EXAMPLE", "id [REDACTED]", []string{"AKIAIOSFODNN7"}},
		{"url userinfo", "git clone https://user:pw123@github.com/o/r", "git clone https://[REDACTED]@github.com/o/r", []string{"pw123", "user:"}},
		{"home", "cat /Users/bob/code/x.go", "cat ~/code/x.go", []string{"/Users/bob"}},
		{"other user kept", "cat /Users/bobby/x", "cat /Users/bobby/x", nil},
		{"short pass key", "DB_PASS=zzz9 run", "DB_PASS=[REDACTED] run", []string{"zzz9"}},
		{"mysql -p space", "mysql -u root -p hunter2 db", "mysql -u root -p [REDACTED] db", []string{"hunter2"}},
		{"mysql -p attached", "mysql -u root -phunter2 db", "mysql -u root -p[REDACTED] db", []string{"hunter2"}},
		{"mkdir -p untouched", "mkdir -p build/out", "mkdir -p build/out", nil},
		{"curl -u", "curl -u bob:pw12 https://x", "curl -u bob:[REDACTED] https://x", []string{"pw12"}},
		{"curl --user", "curl --user bob:pw12 https://x", "curl --user bob:[REDACTED] https://x", []string{"pw12"}},
		{"-u without creds untouched", "tool -u origin main", "tool -u origin main", nil},
		{"stripe key", "echo sk_live_abcdef123456", "echo [REDACTED]", []string{"abcdef"}},
		{"escaped space env", `PASSWORD=a\ bsecret run`, "PASSWORD=[REDACTED] run", []string{"bsecret"}},
		{"escaped space flag", `tool --password a\ bsecret x`, "tool --password [REDACTED] x", []string{"bsecret"}},
		{"clean", "git status --short", "git status --short", nil},
		{"keyboard not secret", "monkey=banana", "monkey=[REDACTED]", nil}, // "key" substring: over-redaction is acceptable
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.in)
			if got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for _, g := range tt.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survived in %q", g, got)
				}
			}
		})
	}
}

func TestPromptRedactsEvidence(t *testing.T) {
	c := &stubComp{out: "ok"}
	n := newTest(t, c)
	r := report("s1", 1)
	r.Findings[0].Evidence = "curl -H 'Authorization: Bearer topsecret1234' https://u:pw@h/"
	if _, err := n.Session(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if p := c.prompts[0]; strings.Contains(p, "topsecret") || strings.Contains(p, "u:pw") {
		t.Errorf("secret in prompt: %s", p)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	nd := filepath.Join(dir, "agentctl", "audit", "narratives")
	if err := os.MkdirAll(nd, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"old.json", "new.json"} {
		if err := os.WriteFile(filepath.Join(nd, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-100 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(nd, "old.json"), past, past); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(time.Now(), MaxAge)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(nd, "new.json")); err != nil {
		t.Error("new narrative removed")
	}
}
