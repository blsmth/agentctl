package launchd

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestJobPlist(t *testing.T) {
	tests := []struct {
		name  string
		job   Job
		check func(t *testing.T, root map[string]any)
	}{
		{
			name: "interval job",
			job: Job{
				Label:            "com.example.tick",
				ProgramArguments: []string{"/bin/agent&ctl", "cron", "tick"},
				RunAtLoad:        true,
				StartInterval:    60,
				LogPath:          "/tmp/x.log",
				Env:              []EnvVar{{"PATH", "/a:/b"}, {"LANG", UTF8Locale}, {"LC_ALL", UTF8Locale}},
			},
			check: func(t *testing.T, root map[string]any) {
				if root["StartInterval"] != "60" || root["RunAtLoad"] != true {
					t.Errorf("root = %v", root)
				}
				if _, ok := root["KeepAlive"]; ok {
					t.Error("KeepAlive set on an interval job")
				}
				args, _ := root["ProgramArguments"].([]any)
				if len(args) != 3 || args[0] != "/bin/agent&ctl" || args[2] != "tick" {
					t.Errorf("args = %v", args)
				}
				env, _ := root["EnvironmentVariables"].(map[string]any)
				if env["PATH"] != "/a:/b" || env["LC_ALL"] != UTF8Locale || env["LANG"] != UTF8Locale {
					t.Errorf("env = %v", env)
				}
				if root["StandardOutPath"] != "/tmp/x.log" || root["StandardErrorPath"] != "/tmp/x.log" {
					t.Errorf("log paths = %v", root)
				}
			},
		},
		{
			name: "minimal job omits optional keys",
			job:  Job{Label: "l", ProgramArguments: []string{"/bin/true"}},
			check: func(t *testing.T, root map[string]any) {
				for _, k := range []string{"RunAtLoad", "KeepAlive", "StartInterval", "EnvironmentVariables", "WorkingDirectory", "ProcessType"} {
					if _, ok := root[k]; ok {
						t.Errorf("%s present", k)
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.job.Plist()
			plutilLint(t, data)
			root, ok := parsePlist(t, data).(map[string]any)
			if !ok {
				t.Fatal("not a dict")
			}
			if root["Label"] != tt.job.Label {
				t.Errorf("Label = %v", root["Label"])
			}
			tt.check(t, root)
		})
	}
}

func TestAgentInstallUninstallLoaded(t *testing.T) {
	home := t.TempDir()
	var calls []string
	failBoot := 1
	a := Agent{Home: home, UID: 501, Sleep: func(time.Duration) {}, Run: func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch args[0] {
		case "bootstrap":
			if failBoot > 0 {
				failBoot--
				return nil, errors.New("exit status 5")
			}
		case "print":
			return nil, errors.New("not loaded")
		}
		return nil, nil
	}}
	path, err := a.Install("com.x", []byte("<plist/>"))
	if err != nil {
		t.Fatal(err)
	}
	if path != PlistPath(home, "com.x") {
		t.Errorf("path = %s", path)
	}
	want := []string{
		"launchctl bootout gui/501/com.x",
		"launchctl bootstrap gui/501 " + path,
		"launchctl bootstrap gui/501 " + path,
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls = %q", calls)
	}
	if a.Loaded("com.x") {
		t.Error("Loaded = true with a failing launchctl print")
	}
	if _, err := a.Uninstall("com.x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("plist not removed")
	}
	if _, err := a.Uninstall("com.x"); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}
