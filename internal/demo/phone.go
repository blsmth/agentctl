package demo

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Phone is the optional phone-app half of a scene: the viewport and theme the
// phone web app is captured in and the steps that drive it. `just
// demo-phone-shot` serves the scene's fleet through the dev server and plays
// the steps with Playwright (WebKit).
type Phone struct {
	Viewport Viewport    `yaml:"viewport"`
	Theme    string      `yaml:"theme"` // light (default) or dark
	Steps    []PhoneStep `yaml:"steps"`
}

// Viewport is the phone's CSS viewport and device pixel ratio. The default is
// an iPhone 15: 393x852 at DPR 3.
type Viewport struct {
	Width  int `yaml:"width" json:"width"`
	Height int `yaml:"height" json:"height"`
	DPR    int `yaml:"dpr" json:"dpr"`
}

// PhoneStep is one phone action. Exactly one field is set. The scalar form
// "screenshot" is accepted as shorthand.
type PhoneStep struct {
	// Open navigates: fleet, window:<key>, terminal:<key> or approval:<key>.
	Open string `yaml:"open"`
	// Tap taps an element: visible text, or "css:<selector>".
	Tap string `yaml:"tap"`
	// Wait waits for an element: visible text, or "css:<selector>".
	Wait string `yaml:"wait"`
	// Screenshot writes a PNG; the value is an optional file suffix.
	Screenshot string `yaml:"screenshot"`

	// shot marks a screenshot step (Screenshot may be empty).
	shot bool
}

var validPhoneThemes = []string{"light", "dark"}

// UnmarshalYAML accepts the screenshot shorthand and a strict mapping.
func (st *PhoneStep) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value != "screenshot" {
			return fmt.Errorf("line %d: unknown phone step %q (want screenshot or a mapping)", n.Line, n.Value)
		}
		st.shot = true
		return nil
	}
	type plain PhoneStep
	var p plain
	if err := strictNode(n, &p); err != nil {
		return err
	}
	*st = PhoneStep(p)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "screenshot" {
			st.shot = true
		}
	}
	return nil
}

// IsScreenshot reports whether the step takes a screenshot.
func (st PhoneStep) IsScreenshot() bool { return st.shot }

// Kind and Arg split the step into its action name and value, for the
// Playwright driver.
func (st PhoneStep) Kind() (kind, arg string) {
	switch {
	case st.Open != "":
		return "open", st.Open
	case st.Tap != "":
		return "tap", st.Tap
	case st.Wait != "":
		return "wait", st.Wait
	}
	return "screenshot", st.Screenshot
}

func (p *Phone) applyDefaults() {
	if p.Viewport.Width == 0 {
		p.Viewport.Width = 393
	}
	if p.Viewport.Height == 0 {
		p.Viewport.Height = 852
	}
	if p.Viewport.DPR == 0 {
		p.Viewport.DPR = 3
	}
	if p.Theme == "" {
		p.Theme = "light"
	}
}

// ParseOpen splits an open target into its kind and window key.
func ParseOpen(target string) (kind, key string) {
	kind, key, _ = strings.Cut(target, ":")
	return kind, key
}

func (p *Phone) validate(s *Scene) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf("phone."+format, a...)) }
	v := p.Viewport
	if v.Width < 200 || v.Width > 1200 {
		add("viewport.width: %d out of range 200-1200", v.Width)
	}
	if v.Height < 300 || v.Height > 2400 {
		add("viewport.height: %d out of range 300-2400", v.Height)
	}
	if v.DPR < 1 || v.DPR > 4 {
		add("viewport.dpr: %d out of range 1-4", v.DPR)
	}
	if !slices.Contains(validPhoneThemes, p.Theme) {
		add("theme: %q is not light or dark", p.Theme)
	}
	if len(p.Steps) == 0 {
		add("steps: at least one step is required")
	}
	shots := 0
	for i, st := range p.Steps {
		if err := st.validate(s); err != nil {
			add("steps[%d]: %v", i, err)
		}
		if st.IsScreenshot() {
			shots++
		}
	}
	if shots == 0 && len(p.Steps) > 0 {
		add("steps: at least one screenshot step is required")
	}
	return errors.Join(errs...)
}

func (st PhoneStep) validate(s *Scene) error {
	set := 0
	for _, b := range []bool{st.Open != "", st.Tap != "", st.Wait != "", st.shot} {
		if b {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("exactly one action per step (open, tap, wait, screenshot), got %d", set)
	}
	switch {
	case st.Open != "":
		return validateOpen(s, st.Open)
	case st.Screenshot != "" && !keyRE.MatchString(st.Screenshot):
		return fmt.Errorf("screenshot: suffix %q must be letters, digits, - or _", st.Screenshot)
	}
	return nil
}

func validateOpen(s *Scene, target string) error {
	if target == "fleet" {
		return nil
	}
	kind, key := ParseOpen(target)
	if !slices.Contains([]string{"window", "terminal", "approval"}, kind) || key == "" {
		return fmt.Errorf("open: %q is not fleet, window:<key>, terminal:<key> or approval:<key>", target)
	}
	w, ok := s.Window(key)
	switch {
	case !ok:
		return fmt.Errorf("open: no window with key %q", key)
	case kind == "approval" && w.State != "blocked":
		return fmt.Errorf("open: approval:%s needs a blocked window (it is %s)", key, w.State)
	}
	return nil
}
