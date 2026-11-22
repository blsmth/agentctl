package demo

import (
	"encoding/json"
	"fmt"
)

// PhonePlan is what the Playwright driver (internal/serve/e2e/shot.mjs)
// plays: the viewport, theme and steps of a scene's phone block, with each
// screenshot step's output path resolved.
type PhonePlan struct {
	Viewport Viewport        `json:"viewport"`
	Theme    string          `json:"theme"`
	Steps    []PhonePlanStep `json:"steps"`
}

// PhonePlanStep is one step: kind is open, tap, wait or screenshot, arg its
// value, and path a screenshot's output file.
type PhonePlanStep struct {
	Kind string `json:"kind"`
	Arg  string `json:"arg,omitempty"`
	Path string `json:"path,omitempty"`
}

// BuildPhonePlan resolves the scene's phone block against base, the output
// path without extension (demo/out/<scene>). A lone unnamed screenshot is
// <base>-phone.png; otherwise each is <base>-phone-<suffix>.png, or
// <base>-phone-<n>.png (n counts all screenshots from 1) when it has no suffix.
func BuildPhonePlan(s *Scene, base string) (*PhonePlan, []string, error) {
	if s.Phone == nil {
		return nil, nil, fmt.Errorf("scene %s has no phone block", s.Name)
	}
	shots := 0
	for _, st := range s.Phone.Steps {
		if st.IsScreenshot() {
			shots++
		}
	}
	plan := &PhonePlan{Viewport: s.Phone.Viewport, Theme: s.Phone.Theme}
	var outs []string
	n := 0
	for _, st := range s.Phone.Steps {
		kind, arg := st.Kind()
		step := PhonePlanStep{Kind: kind}
		if kind == "screenshot" {
			n++
			name := base + "-phone"
			switch {
			case arg != "":
				name += "-" + arg
			case shots > 1:
				name += fmt.Sprintf("-%d", n)
			}
			step.Path = name + ".png"
			outs = append(outs, step.Path)
		} else {
			step.Arg = arg
		}
		plan.Steps = append(plan.Steps, step)
	}
	return plan, outs, nil
}

// JSON encodes the plan for the driver.
func (p *PhonePlan) JSON() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }
