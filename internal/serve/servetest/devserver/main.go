// Command devserver runs the phone web app on fake backends for browser
// tests and manual poking: the real serve HTTP handler (Router, middleware,
// embedded assets) over the servetest fixture fleet, on plain HTTP bound to
// loopback. It never touches tmux, real state dirs or the serve socket.
//
//	go run ./internal/serve/servetest/devserver -addr 127.0.0.1:8787
//
// With -scene it serves a demo scene's fleet instead of the default fixture
// (internal/demo/phonefix): `just demo-phone-shot` drives it with Playwright.
// It then also serves GET /__scene, the scene's windows and their approvals.
//
// It prints the app URL (with a one-time login link that sets the device
// cookie) and serves until interrupted. Not part of the agentctl binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/serve"
	"github.com/blsmth/agentctl/internal/serve/servetest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devserver:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8787", "loopback address to listen on")
	readOnly := flag.Bool("read-only", false, "turn serve.allow_write off")
	sceneArg := flag.String("scene", "", "demo scene (name in -scenes, or a path containing a /) to serve instead of the default fixture")
	scenes := flag.String("scenes", "demo/scenes", "scene directory for -scene")
	fixtures := flag.String("fixtures", "demo/transcripts", "transcript fixture directory for -scene")
	flag.Parse()

	var sc *scene
	if *sceneArg != "" {
		var err error
		if sc, err = loadScene(*scenes, *sceneArg, *fixtures); err != nil {
			return err
		}
	}

	ln, err := listenLoopback(*addr)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "agentctl-devserver-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// Before anything logs: keep serve's log out of the real state dirs.
	logging.InitFile(filepath.Join(dir, "serve.log"))

	opts := servetest.Options{Dir: dir, ReadOnly: *readOnly}
	if sc != nil {
		opts.Fleet = sc.groups
	}
	h, err := servetest.New(opts)
	if err != nil {
		return err
	}
	defer h.Close()
	if sc != nil {
		if err := sc.openApprovals(h); err != nil {
			return err
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /__login", func(w http.ResponseWriter, r *http.Request) {
		// For a desktop browser: the device cookie, minus Secure (plain
		// HTTP), then the app.
		http.SetCookie(w, &http.Cookie{
			Name: serve.DeviceCookie, Value: h.Token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	if sc != nil {
		mux.HandleFunc("GET /__scene", sc.serve)
	}
	mux.Handle("/", h.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	base := "http://" + ln.Addr().String()
	fmt.Printf("devserver: app   %s/\n", base)
	fmt.Printf("devserver: login %s/__login (sets the device cookie)\n", base)
	fmt.Printf("devserver: state %s\n", dir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	return nil
}

// listenLoopback listens on addr, refusing any host that is not loopback.
func listenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid -addr %q: %w", addr, err)
	}
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("-addr %q: the dev server only binds loopback", addr)
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("-addr %q resolved to a non-loopback address", addr)
	}
	return ln, nil
}
