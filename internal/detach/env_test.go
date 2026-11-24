package detach

import (
	"slices"
	"testing"
)

func TestStripAccountEnv(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "named account window",
			in:   []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/a/work", "AGENTCTL_ACCOUNT=work", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1", "HOME=/h"},
			want: []string{"PATH=/bin", "HOME=/h"},
		},
		{
			name: "scrubbed window (config dir already dropped)",
			in:   []string{"AGENTCTL_ACCOUNT=work", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1", "PATH=/bin"},
			want: []string{"PATH=/bin"},
		},
		{
			name: "machine-wide config dir without an account is kept",
			in:   []string{"CLAUDE_CONFIG_DIR=/a/mine", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1", "PATH=/bin"},
			want: []string{"CLAUDE_CONFIG_DIR=/a/mine", "PATH=/bin"},
		},
		{
			name: "empty account still marks a per-window env",
			in:   []string{"CLAUDE_CONFIG_DIR=/a/x", "AGENTCTL_ACCOUNT="},
			want: []string{},
		},
		{
			name: "prefix-alike names untouched",
			in:   []string{"AGENTCTL_ACCOUNTS=x", "CLAUDE_CONFIG_DIRS=y"},
			want: []string{"AGENTCTL_ACCOUNTS=x", "CLAUDE_CONFIG_DIRS=y"},
		},
		{name: "nil", in: nil, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := slices.Clone(tt.in)
			got := StripAccountEnv(in)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("StripAccountEnv(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !slices.Equal(in, tt.in) {
				t.Fatalf("input mutated: %q", in)
			}
		})
	}
}

func TestDaemonEnv(t *testing.T) {
	in := []string{"AGENTCTL_RUNTIME_DIR=/old", "AGENTCTL_ACCOUNT=work", "CLAUDE_CONFIG_DIR=/a/work", "PATH=/bin"}
	if got, want := DaemonEnv(in, "/new"), []string{"PATH=/bin", "AGENTCTL_RUNTIME_DIR=/new"}; !slices.Equal(got, want) {
		t.Fatalf("DaemonEnv pinned = %q, want %q", got, want)
	}
	if got, want := DaemonEnv(in, ""), []string{"AGENTCTL_RUNTIME_DIR=/old", "PATH=/bin"}; !slices.Equal(got, want) {
		t.Fatalf("DaemonEnv unpinned = %q, want %q", got, want)
	}
}

// captureStartSelf stubs StartSelf and returns what it was called with.
func captureStartSelf(t *testing.T) (args, env *[]string) {
	t.Helper()
	var a, e []string
	restore := SetStartSelfForTest(func(args, env []string) error {
		a, e = args, env
		return nil
	})
	t.Cleanup(restore)
	return &a, &e
}

func TestStartDaemonStripsAccount(t *testing.T) {
	t.Setenv(EnvConfigDir, "/a/work")
	t.Setenv(EnvAccount, "work")
	t.Setenv(EnvSubprocessScrub, "1")
	args, env := captureStartSelf(t)
	if err := StartDaemon([]string{"reconcile"}, "/rt"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*args, []string{"reconcile"}) {
		t.Fatalf("args = %q", *args)
	}
	for _, name := range []string{EnvConfigDir, EnvAccount, EnvSubprocessScrub} {
		if hasVar(*env, name) {
			t.Errorf("child env keeps %s", name)
		}
	}
	if !slices.Contains(*env, "AGENTCTL_RUNTIME_DIR=/rt") {
		t.Errorf("child env lacks pinned runtime dir: %q", *env)
	}
}

func TestDaemonEnvStripsSessionID(t *testing.T) {
	in := []string{"PATH=/bin", "AGENTCTL_SESSION_ID=abc", "AGENTCTL_SESSION_IDX=keep"}
	want := []string{"PATH=/bin", "AGENTCTL_SESSION_IDX=keep"}
	if got := DaemonEnv(in, ""); !slices.Equal(got, want) {
		t.Fatalf("DaemonEnv = %q, want %q", got, want)
	}
}

func TestStartDaemonDropsSessionID(t *testing.T) {
	t.Setenv(EnvSessionID, "sess-1")
	_, env := captureStartSelf(t)
	if err := StartDaemon([]string{"reconcile", "--housekeeping"}, "/rt"); err != nil {
		t.Fatal(err)
	}
	if hasVar(*env, EnvSessionID) {
		t.Fatalf("child env keeps %s: %q", EnvSessionID, *env)
	}
}
