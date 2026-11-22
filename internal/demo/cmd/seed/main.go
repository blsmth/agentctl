// Command seed is the agentctl demo harness (built as agentctl-demo by
// `just demo`). It seeds a fake, isolated agentctl world from a scene file
// and renders scenes to PNG/GIF with vhs. See docs/development.md
// ("Demo harness and screenshots") and demo/scenes/README.md.
//
//	agentctl-demo up    --root R --scene S [--scenes D] [--fixtures F]
//	agentctl-demo shot  --root R --scene S [--out BASE] [--vhs PATH]
//	agentctl-demo tape  --root R --scene S [--out BASE]
//	agentctl-demo phone-shot --scene S [--out BASE]
//	agentctl-demo check --scene S
//	agentctl-demo down  --root R
//	agentctl-demo pane-check --target T
//
// "project" and "pane" are internal: up runs one project seeder per project,
// and every fake window runs a pane.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/demo"
	"github.com/blsmth/agentctl/internal/selfpath"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "up":
		err = runUp(args)
	case "down":
		err = runDown(args)
	case "shot":
		err = runShot(args)
	case "phone-shot":
		err = runPhoneShot(args)
	case "tape":
		err = runTape(args)
	case "check":
		err = runCheck(args)
	case "project":
		err = runProject(args)
	case "pane":
		err = runPane(args)
	case "pane-check":
		err = runPaneCheck(args)
	case "serve-stub":
		err = runServeStub(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentctl-demo %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agentctl-demo up|shot|phone-shot|tape|check|down [flags]")
	os.Exit(2)
}

// common are the flags shared by the scene commands.
type common struct {
	root, scene, scenes, fixtures string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.root, "root", demo.DefaultRoot(), "demo temp root")
	fs.StringVar(&c.scene, "scene", "default", "scene name (in --scenes) or path to a scene file")
	fs.StringVar(&c.scenes, "scenes", "demo/scenes", "scene directory")
	fs.StringVar(&c.fixtures, "fixtures", "demo/transcripts", "transcript fixture directory")
}

// scenePath resolves --scene: a bare name is looked up in --scenes (with or
// without .yaml), anything containing a slash is a path.
func (c *common) scenePath() (string, error) {
	return demo.ResolveScene(c.scenes, c.scene)
}

func (c *common) load() (demo.Layout, *demo.Scene, string, error) {
	l, err := demo.NewLayout(c.root)
	if err != nil {
		return l, nil, "", err
	}
	path, err := c.scenePath()
	if err != nil {
		return l, nil, "", err
	}
	s, err := demo.LoadScene(path)
	if err != nil {
		return l, nil, "", err
	}
	if c.fixtures, err = filepath.Abs(c.fixtures); err != nil {
		return l, nil, "", err
	}
	return l, s, path, nil
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

func up(c *common) (demo.Layout, *demo.Scene, *demo.Manifest, error) {
	l, s, path, err := c.load()
	if err != nil {
		return l, nil, nil, err
	}
	self, err := selfpath.Spawnable()
	if err != nil {
		return l, nil, nil, err
	}
	m, err := demo.Up(l, s, demo.UpOptions{ScenePath: path, Fixtures: c.fixtures, Self: self})
	return l, s, m, err
}

func runUp(args []string) error {
	var c common
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	c.register(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	l, s, m, err := up(&c)
	if err != nil {
		return err
	}
	first := s.Projects[0].Name
	fmt.Printf("demo scene %q is up under %s\n\n%s\n", s.Name, l.Root, m.Summary())
	fmt.Printf("attach:  %s\nstop:    just demo-stop\n", l.AttachCommand(m.Sessions[first]))
	return nil
}

func runDown(args []string) error {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	root := fs.String("root", demo.DefaultRoot(), "demo temp root")
	if err := parse(fs, args); err != nil {
		return err
	}
	l, err := demo.NewLayout(*root)
	if err != nil {
		return err
	}
	if err := demo.Down(l); err != nil {
		return err
	}
	fmt.Printf("demo stopped; removed %s\n", l.Root)
	return nil
}

func runCheck(args []string) error {
	var c common
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	c.register(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	path, err := c.scenePath()
	if err != nil {
		return err
	}
	s, err := demo.LoadScene(path)
	if err != nil {
		return err
	}
	fixtures, err := filepath.Abs(c.fixtures)
	if err != nil {
		return err
	}
	for _, w := range s.Windows {
		if _, err := w.Transcript.Resolve(fixtures); err != nil {
			return fmt.Errorf("window %s: %w", w.Key, err)
		}
	}
	fmt.Printf("%s: ok\n", path)
	return nil
}

// outBase is the output path without extension: --out, else
// demo/out/<scene>.
func outBase(out, scene string) (string, error) {
	if out == "" {
		out = filepath.Join("demo", "out", scene)
	}
	out = strings.TrimSuffix(strings.TrimSuffix(out, ".png"), ".gif")
	return filepath.Abs(out)
}

func runTape(args []string) error {
	var c common
	fs := flag.NewFlagSet("tape", flag.ExitOnError)
	c.register(fs)
	out := fs.String("out", "", "output path without extension (default demo/out/<scene>)")
	if err := parse(fs, args); err != nil {
		return err
	}
	l, s, _, err := c.load()
	if err != nil {
		return err
	}
	m, err := demo.LoadManifest(l)
	if err != nil {
		return err
	}
	base, err := outBase(*out, s.Name)
	if err != nil {
		return err
	}
	tape, _, err := demo.Tape(s, m, base, filepath.Join(l.Tmp(), s.Name+".gif"))
	if err != nil {
		return err
	}
	fmt.Print(tape)
	return nil
}

func runShot(args []string) error {
	var c common
	fs := flag.NewFlagSet("shot", flag.ExitOnError)
	c.register(fs)
	out := fs.String("out", "", "output path without extension (default demo/out/<scene>)")
	vhs := fs.String("vhs", "", "vhs binary (default: vhs on PATH, then /opt/homebrew/bin/vhs)")
	if err := parse(fs, args); err != nil {
		return err
	}
	l, s, m, err := up(&c)
	if err != nil {
		return err
	}
	base, err := outBase(*out, s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return err
	}
	tape, outs, err := demo.Tape(s, m, base, filepath.Join(l.Tmp(), s.Name+".gif"))
	if err != nil {
		return err
	}
	tapePath := filepath.Join(l.Tmp(), s.Name+".tape")
	if err := os.WriteFile(tapePath, []byte(tape), 0o600); err != nil {
		return err
	}
	bin, err := vhsBinary(*vhs)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, tapePath)
	cmd.Env = vhsEnv()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vhs %s: %w", tapePath, err)
	}
	for _, p := range append(outs.Shots, outs.GIF) {
		if p != "" {
			fmt.Println(p)
		}
	}
	return nil
}

func vhsBinary(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if p, err := exec.LookPath("vhs"); err == nil {
		return p, nil
	}
	if _, err := os.Stat("/opt/homebrew/bin/vhs"); err == nil {
		return "/opt/homebrew/bin/vhs", nil
	}
	return "", fmt.Errorf("vhs not found; install it (brew install vhs) or pass --vhs")
}

// vhsEnv is the caller's environment without tmux, agentctl and Claude
// variables: vhs needs the real HOME (its browser cache), but its shell must
// not inherit a live session's identity.
func vhsEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "TMUX" || k == "TMUX_PANE" || strings.HasPrefix(k, "AGENTCTL") || strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "ANTHROPIC_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// runProject is the per-project child of up. Its environment comes from up
// and carries the demo TMUX; it is replaced wholesale before anything runs.
func runProject(args []string) error {
	var c common
	fs := flag.NewFlagSet("project", flag.ExitOnError)
	c.register(fs)
	project := fs.String("project", "", "project to seed")
	label := fs.String("label", "", "tmux socket label, as computed by up (never re-derived here)")
	if err := parse(fs, args); err != nil {
		return err
	}
	l, s, _, err := c.load()
	if err != nil {
		return err
	}
	l = l.WithLabel(*label)
	sock := os.Getenv("TMUX")
	if !strings.Contains(sock, "/"+l.Label()+",") {
		return fmt.Errorf("TMUX=%q is not the demo server; run through `agentctl-demo up`", sock)
	}
	if err := l.Isolate(sock); err != nil {
		return err
	}
	self, err := selfpath.Spawnable()
	if err != nil {
		return err
	}
	return demo.SeedProject(l, s, *project, self, os.Stdout)
}

func runPane(args []string) error {
	fs := flag.NewFlagSet("pane", flag.ExitOnError)
	spec := fs.String("spec", "", "pane spec file")
	if err := parse(fs, args); err != nil {
		return err
	}
	return demo.RunPane(*spec)
}

// runPaneCheck compares a real Claude Code pane's structure with the fake's
// and exits non-zero on a mismatch. It prints marker names only.
func runPaneCheck(args []string) error {
	fs := flag.NewFlagSet("pane-check", flag.ExitOnError)
	target := fs.String("target", "", "tmux target of a real Claude Code pane (default tmux server)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	onlyReal, onlyFake, err := demo.CheckPane(*target)
	if err != nil {
		return err
	}
	for _, m := range onlyReal {
		fmt.Printf("real pane has, fake lacks: %s\n", m)
	}
	for _, m := range onlyFake {
		fmt.Printf("fake pane has, real lacks: %s\n", m)
	}
	if len(onlyReal)+len(onlyFake) > 0 {
		return fmt.Errorf("fake pane drifted from the real one (update internal/demo/pane.go)")
	}
	fmt.Println("pane structure matches")
	return nil
}

// runServeStub is the fake agentctl serve behind the hub's serve badge.
func runServeStub(args []string) error {
	fs := flag.NewFlagSet("serve-stub", flag.ExitOnError)
	dir := fs.String("dir", "", "serve dir to listen in")
	if err := parse(fs, args); err != nil {
		return err
	}
	return demo.RunServeStub(*dir)
}
