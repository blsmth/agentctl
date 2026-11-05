package hooks

import (
	"slices"
	"testing"

	"mvdan.cc/sh/v3/shell"
)

// hostilePopupValues are values that would split, break, or execute if
// interpolated into a display-popup command unquoted.
var hostilePopupValues = []string{
	"plain",
	"/Users/Jane Doe/bin/agentctl",
	"x; touch /tmp/pwned",
	"$(touch /tmp/pwned)",
	"`id`",
	`it's "quoted"`,
	"line1\nline2",
	"$HOME",
	"",
}

func popupFields(t *testing.T, cmd string) []string {
	t.Helper()
	got, err := shell.Fields(cmd, func(string) string { return "EXPANDED" })
	if err != nil {
		t.Fatalf("shell.Fields(%q): %v", cmd, err)
	}
	return got
}

func TestFilePreviewPopupCmdRoundTrips(t *testing.T) {
	for _, v := range hostilePopupValues {
		t.Run(v, func(t *testing.T) {
			cmd := filePreviewPopupCmd(v, v+"-tmp", v+"-file", v+"-agent", 80, 24)
			want := []string{
				v, "file-preview",
				"--path", v + "-tmp",
				"--name", v + "-file",
				"--agent", v + "-agent",
				"--content-width", "80",
				"--vp-height", "24",
			}
			if got := popupFields(t, cmd); !slices.Equal(got, want) {
				t.Errorf("filePreviewPopupCmd = %s\n tokenizes to %q\n want         %q", cmd, got, want)
			}
		})
	}
}

func TestDispatchPreviewPopupCmdRoundTrips(t *testing.T) {
	for _, v := range hostilePopupValues {
		t.Run(v, func(t *testing.T) {
			cmd := dispatchPreviewPopupCmd(v, v+"/args.json")
			want := []string{v, "dispatch-preview", "--args-file", v + "/args.json"}
			if got := popupFields(t, cmd); !slices.Equal(got, want) {
				t.Errorf("dispatchPreviewPopupCmd = %s\n tokenizes to %q\n want         %q", cmd, got, want)
			}
		})
	}
}
