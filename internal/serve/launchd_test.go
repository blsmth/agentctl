package serve

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPlist(t *testing.T) {
	spec := ServiceSpec{Exe: "/Users/me/.local/bin/agent&ctl", Dir: "/Users/me/.local/state/agentctl/serve", Path: "/opt/homebrew/bin:/usr/bin"}
	data := Plist(spec)
	plutilLint(t, data)
	root, ok := parsePlist(t, data).(map[string]any)
	if !ok {
		t.Fatal("not a dict")
	}
	if root["Label"] != LaunchdLabel || root["RunAtLoad"] != true || root["KeepAlive"] != true {
		t.Errorf("plist = %v", root)
	}
	args, _ := root["ProgramArguments"].([]any)
	if len(args) != 2 || args[0] != spec.Exe || args[1] != "serve" {
		t.Errorf("ProgramArguments = %v", args)
	}
	if root["WorkingDirectory"] != spec.Dir {
		t.Errorf("WorkingDirectory = %v", root["WorkingDirectory"])
	}
	env, _ := root["EnvironmentVariables"].(map[string]any)
	if env["PATH"] != spec.Path {
		t.Errorf("PATH = %v", env["PATH"])
	}
}

func TestSystemdUnit(t *testing.T) {
	unit := SystemdUnit(ServiceSpec{Exe: "/home/me/my bin/agentctl", Dir: "/home/me/.local/state/agentctl/serve", Path: "/usr/bin"})
	for _, want := range []string{`ExecStart="/home/me/my bin/agentctl" serve`, "Restart=always", "WantedBy=default.target", "Environment=PATH=/usr/bin"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

type recordedRun struct{ calls [][]string }

func (r *recordedRun) runner(fail map[string]int) runner {
	return func(name string, args ...string) ([]byte, error) {
		r.calls = append(r.calls, append([]string{name}, args...))
		if fail[args[0]] > 0 {
			fail[args[0]]--
			return []byte("Input/output error"), errors.New("exit status 5")
		}
		return nil, nil
	}
}

func TestLaunchdInstallUninstall(t *testing.T) {
	tests := []struct {
		name        string
		failBoot    int
		wantErr     bool
		wantBootTry int
	}{
		{name: "clean install", wantBootTry: 1},
		{name: "bootstrap retried while old job unloads", failBoot: 2, wantBootTry: 3},
		{name: "bootstrap keeps failing", failBoot: 99, wantErr: true, wantBootTry: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			rec := &recordedRun{}
			l := &Launchd{home: home, uid: 501, run: rec.runner(map[string]int{"bootstrap": tt.failBoot}), sleep: func(time.Duration) {}}
			path, err := l.Install(ServiceSpec{Exe: "/bin/agentctl", Dir: "/tmp/x"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Install err = %v", err)
			}
			if path != PlistPath(home) {
				t.Errorf("path = %s", path)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("plist not written: %v", err)
			}
			if got := strings.Join(rec.calls[0], " "); got != "launchctl bootout gui/501/"+LaunchdLabel {
				t.Errorf("first call = %q", got)
			}
			boots := 0
			for _, c := range rec.calls[1:] {
				if strings.Join(c, " ") != "launchctl bootstrap gui/501 "+path {
					t.Errorf("unexpected call %v", c)
				}
				boots++
			}
			if boots != tt.wantBootTry {
				t.Errorf("bootstrap attempts = %d, want %d", boots, tt.wantBootTry)
			}

			rec.calls = nil
			if _, err := l.Uninstall(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("plist not removed")
			}
			if len(rec.calls) != 1 || rec.calls[0][1] != "bootout" {
				t.Errorf("uninstall calls = %v", rec.calls)
			}
			// Uninstalling twice is fine.
			if _, err := l.Uninstall(); err != nil {
				t.Errorf("second uninstall: %v", err)
			}
		})
	}
}
