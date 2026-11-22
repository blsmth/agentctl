package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

func stubTemplate(t *testing.T, tmpl prompttemplate.Template, err error) *string {
	t.Helper()
	old := loadPromptTemplate
	t.Cleanup(func() { loadPromptTemplate = old })
	var gotDir string
	loadPromptTemplate = func(name, dir string) (prompttemplate.Template, error) {
		gotDir = dir
		if err != nil {
			return prompttemplate.Template{}, err
		}
		tmpl.Name = name
		return tmpl, nil
	}
	return &gotDir
}

func TestApplyDispatchTemplate(t *testing.T) {
	full := prompttemplate.Template{Agent: "writer", Model: "opus", Dir: "~/code/site", Effort: "high", Prompt: "Review open PRs."}
	tests := []struct {
		name    string
		tmpl    prompttemplate.Template
		opts    DispatchOptions
		want    DispatchOptions
		wantErr string
	}{
		{
			name: "template defaults fill unset flags",
			tmpl: full,
			opts: DispatchOptions{Template: "t"},
			want: DispatchOptions{Template: "t", Agent: "writer", Model: "opus", Dir: paths.ExpandHome("~/code/site"), Effort: "high", Prompt: "Review open PRs."},
		},
		{
			name: "flags beat template defaults",
			tmpl: full,
			opts: DispatchOptions{Template: "t", Agent: "go-engineer", Model: "haiku", Dir: "/x", Effort: "low"},
			want: DispatchOptions{Template: "t", Agent: "go-engineer", Model: "haiku", Dir: "/x", Effort: "low", Prompt: "Review open PRs."},
		},
		{
			name: "prompt is appended, not a replacement",
			tmpl: full,
			opts: DispatchOptions{Template: "t", Prompt: "Only PRs by me."},
			want: DispatchOptions{Template: "t", Agent: "writer", Model: "opus", Dir: paths.ExpandHome("~/code/site"), Effort: "high",
				Prompt: "Review open PRs.\n\n" + dispatch.AdditionalInstructionsHeading + "\n\nOnly PRs by me."},
		},
		{
			name:    "no agent anywhere",
			tmpl:    prompttemplate.Template{Prompt: "x"},
			opts:    DispatchOptions{Template: "t"},
			wantErr: "sets no agent; pass --agent",
		},
		{
			name: "agent flag covers a template without one",
			tmpl: prompttemplate.Template{Prompt: "x"},
			opts: DispatchOptions{Template: "t", Agent: "a"},
			want: DispatchOptions{Template: "t", Agent: "a", Prompt: "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir := stubTemplate(t, tt.tmpl, nil)
			got, err := applyDispatchTemplate(tt.opts, "/proj")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
			if *gotDir != "/proj" {
				t.Errorf("loaded from %q, want the cwd", *gotDir)
			}
		})
	}
}

func TestApplyDispatchTemplate_NotFound(t *testing.T) {
	stubTemplate(t, prompttemplate.Template{}, prompttemplate.ErrNotFound)
	_, err := applyDispatchTemplate(DispatchOptions{Template: "gone"}, "/proj")
	if !errors.Is(err, prompttemplate.ErrNotFound) || !strings.Contains(err.Error(), "--template gone") {
		t.Errorf("err = %v", err)
	}
}

// The heading a --prompt is added under is the phone's wording.
func TestTemplateHeadingMatchesDispatch(t *testing.T) {
	if prompttemplate.AdditionalInstructionsHeading != dispatch.AdditionalInstructionsHeading {
		t.Errorf("prompttemplate heading %q != dispatch heading %q",
			prompttemplate.AdditionalInstructionsHeading, dispatch.AdditionalInstructionsHeading)
	}
}

// The template name reaches the launch as display-only metadata.
func TestDispatchPromptOptionsPassesTemplate(t *testing.T) {
	old := launchAdhoc
	t.Cleanup(func() { launchAdhoc = old })
	var got dispatch.AdhocRequest
	launchAdhoc = func(req dispatch.AdhocRequest) (dispatch.Result, error) {
		got = req
		return dispatch.Result{WindowID: "@1", AgentName: req.AgentName}, nil
	}
	dir := t.TempDir()
	if _, _, err := dispatchPromptOptions(DispatchOptions{Prompt: "p", Template: "t", Agent: "a", Dir: dir, Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if got.Template != "t" || got.Effort != "high" || got.Prompt != "p" {
		t.Errorf("AdhocRequest = %+v", got)
	}
}
