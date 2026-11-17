package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// Files under the serve dir.
const (
	PIDFile = "serve.pid"
	LogFile = "serve.log"
)

// reloadEvery is how often the server checks whether `just install`
// replaced its binary.
const reloadEvery = 2 * time.Second

// Dir returns the serve state dir, <StateDir>/serve.
func Dir() (string, error) {
	state, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "serve"), nil
}

// EnsureDir creates dir 0700 (tightening an existing one).
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// RunOptions configures Run.
type RunOptions struct {
	Version string
	// Out receives the one-line startup banner (launchd sends it to
	// launchd.log).
	Out io.Writer
}

// Run runs agentctl serve in the foreground until SIGINT/SIGTERM or ctx is
// done. When the binary on disk is replaced it shuts down its listeners and
// re-execs the new binary in place (same PID), so `just install` upgrades a
// running server.
func Run(ctx context.Context, opts RunOptions) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := EnsureDir(dir); err != nil {
		return err
	}
	if pid, ok := pidfile.Running(dir, PIDFile, "serve"); ok {
		return fmt.Errorf("agentctl serve is already running (pid %d)", pid)
	}
	logging.InitFile(filepath.Join(dir, LogFile))

	cfg := config.Load().Serve
	now := time.Now()
	hostname, _ := os.Hostname()
	ca, err := LoadOrCreateCA(dir, hostname, cfg.Hostnames, now)
	if err != nil {
		return err
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		logging.Warn("serve: listing interface addresses: %v", err)
	}
	sans := DesiredSANs(cfg.Hostnames, hostname, addrs)
	leaf, regenerated, err := EnsureLeaf(dir, ca, sans, now)
	if err != nil {
		return err
	}
	if !ca.Constrained() {
		logging.Warn("serve: the CA has no name constraints; run `agentctl serve ca rotate` to replace it")
	}
	if regenerated {
		logging.Log("serve: issued server certificate for %v %v", sans.DNS, sans.IPs)
	}

	listen := cfg.EffectiveListen()
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", listen, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	listenHost, _, _ := net.SplitHostPort(listen)

	srv, err := New(Options{
		Config:        cfg,
		Dir:           dir,
		Version:       opts.Version,
		CA:            ca,
		AdvertiseHost: advertiseHost(sans),
		HTTPSPort:     port,
		BootstrapAddr: net.JoinHostPort(listenHost, strconv.Itoa(port+1)),
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	defer srv.Close()

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{leaf}}
	httpSrv := newHTTPServer(srv.Handler())
	httpSrv.TLSConfig = tlsCfg
	// TLS handshake failures (a phone that does not trust the CA yet) are
	// routine; keep them out of the main log unless AGENTCTL_DEBUG=1.
	httpSrv.ErrorLog = log.New(debugLogWriter{}, "", 0)
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(tls.NewListener(ln, tlsCfg)) }()

	ctl, err := listenControl(dir)
	if err != nil {
		shutdown(httpSrv)
		return err
	}
	go serveControl(ctl, srv)

	if err := pidfile.Write(dir, PIDFile); err != nil {
		logging.Warn("serve: %v", err)
	}

	exe, reloadErr := selfexec.Capture()
	if reloadErr != nil {
		logging.Log("serve: hot reload disabled: %v", reloadErr)
	}

	st := srv.Status()
	logging.Log("serve: listening on %s (pid %d, version %s)", st.URL, st.PID, opts.Version)
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "agentctl serve listening on %s (CA %s)\n", st.URL, ca.Fingerprint())
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	tick := time.NewTicker(reloadEvery)
	defer tick.Stop()

	stop := func() {
		_ = ctl.Close()
		shutdown(httpSrv)
		srv.Close()
	}
	for {
		select {
		case <-ctx.Done():
			stop()
			pidfile.Remove(dir, PIDFile)
			return nil
		case s := <-sigs:
			logging.Log("serve: %s, shutting down", s)
			stop()
			pidfile.Remove(dir, PIDFile)
			return nil
		case err := <-serveErr:
			_ = ctl.Close()
			srv.Close()
			pidfile.Remove(dir, PIDFile)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("https server: %w", err)
		case <-srv.restart:
			// `serve ca rotate` replaced the certificates on disk.
			path := exe.Path
			if reloadErr != nil {
				path = selfpath.Path()
			}
			logging.Log("serve: restart requested, re-executing %s", path)
			stop()
			return reexec(dir, path)
		case t := <-tick.C:
			if reloadErr != nil || !selfexec.CheckChanged(exe, t) {
				continue
			}
			logging.Log("serve: binary changed, re-executing %s", exe.Path)
			stop()
			return reexec(dir, exe.Path)
		}
	}
}

// reexec replaces the process with path. Same PID: serve.pid stays valid.
// Exec returns only on failure; exiting then lets launchd's KeepAlive start
// the server again.
func reexec(dir, path string) error {
	err := selfexec.Reexec(path)
	pidfile.Remove(dir, PIDFile)
	return fmt.Errorf("re-exec %s: %w", path, err)
}

// shutdown stops srv gracefully, giving in-flight requests a few seconds.
func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
}

// advertiseHost picks the host for pairing URLs: the address of the
// interface that carries the default route when the certificate covers it,
// else the first IPv4 SAN, else the first DNS SAN.
func advertiseHost(sans SANs) string {
	if ip := primaryIP(); ip != nil {
		for _, s := range sans.IPs {
			if s.Equal(ip) {
				return ip.String()
			}
		}
	}
	for _, ip := range sans.IPs {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	if len(sans.DNS) > 0 {
		return sans.DNS[0]
	}
	return "localhost"
}

// primaryIP returns the local address the OS would use to reach the
// internet. Dialing UDP sends no packets; it only selects a route.
var primaryIP = func() net.IP {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return nil
	}
	defer func() { _ = c.Close() }()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

// debugLogWriter sends net/http's internal error log to logging.Debug.
type debugLogWriter struct{}

func (debugLogWriter) Write(p []byte) (int, error) {
	logging.Debug("serve: http: %s", p)
	return len(p), nil
}
