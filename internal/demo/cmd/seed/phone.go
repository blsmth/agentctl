package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/demo"
)

var appURLRE = regexp.MustCompile(`^devserver: app\s+(http://\S+)/$`)

// runPhoneShot serves the scene's fleet on the dev server (loopback, free
// port, no tmux or real state) and plays its phone steps with Playwright. It
// runs from the repo root and always stops the server.
func runPhoneShot(args []string) error {
	var c common
	fs := flag.NewFlagSet("phone-shot", flag.ExitOnError)
	c.register(fs)
	out := fs.String("out", "", "output path without extension (default demo/out/<scene>)")
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
	base, err := outBase(*out, s.Name)
	if err != nil {
		return err
	}
	plan, outs, err := demo.BuildPhonePlan(s, base)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "agentctl-phone-shot-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	planFile := filepath.Join(tmp, "plan.json")
	data, err := plan.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(planFile, data, 0o600); err != nil {
		return err
	}

	dev := filepath.Join(tmp, "devserver")
	build := exec.Command("go", "build", "-o", dev, "./internal/serve/servetest/devserver")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("building the dev server: %w", err)
	}
	fixtures, err := filepath.Abs(c.fixtures)
	if err != nil {
		return err
	}
	url, stop, err := startDevserver(dev, path, fixtures)
	if err != nil {
		return err
	}
	defer stop()

	node := exec.Command("node", filepath.Join("internal", "serve", "e2e", "shot.mjs"), url, planFile)
	node.Env = vhsEnv()
	node.Stdout, node.Stderr = os.Stderr, os.Stderr
	if err := node.Run(); err != nil {
		return fmt.Errorf("driving the phone app: %w", err)
	}
	for _, p := range outs {
		fmt.Println(p)
	}
	return nil
}

// startDevserver runs the dev server on scene and returns its base URL and a
// stop function that ends it.
func startDevserver(bin, scene, fixtures string) (string, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "-addr", "127.0.0.1:0", "-scene", scene, "-fixtures", fixtures)
	cmd.Env = vhsEnv()
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, fmt.Errorf("starting the dev server: %w", err)
	}
	stop := func() {
		cancel()
		_ = cmd.Wait()
	}
	urls := make(chan string, 1)
	go func() {
		defer close(urls)
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() { // keep draining so the server never blocks on stdout
			if m := appURLRE.FindStringSubmatch(sc.Text()); m != nil && !sent {
				urls <- m[1]
				sent = true
			}
		}
	}()
	select {
	case u, ok := <-urls:
		if !ok {
			stop()
			return "", nil, fmt.Errorf("the dev server exited before printing its URL")
		}
		return u, stop, nil
	case <-time.After(30 * time.Second):
		stop()
		return "", nil, fmt.Errorf("the dev server did not print its URL within 30s")
	}
}
