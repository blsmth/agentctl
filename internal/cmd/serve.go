package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/pidfile"
	"github.com/blsmth/agentctl/internal/serve"
)

// RunServe runs agentctl serve in the foreground.
func RunServe(version string) error {
	return serve.Run(context.Background(), serve.RunOptions{Version: version, Out: os.Stdout})
}

// serveDir resolves and creates the serve state dir.
func serveDir() (string, error) {
	dir, err := serve.Dir()
	if err != nil {
		return "", err
	}
	return dir, serve.EnsureDir(dir)
}

// RunServeInstall installs agentctl serve as a launchd LaunchAgent (macOS)
// or prints a systemd user unit (elsewhere).
func RunServeInstall(w io.Writer) error {
	dir, err := serveDir()
	if err != nil {
		return err
	}
	spec, err := serve.CurrentServiceSpec(dir)
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintf(w, "# Save as ~/.config/systemd/user/agentctl-serve.service, then run:\n")
		fmt.Fprintf(w, "#   systemctl --user daemon-reload && systemctl --user enable --now agentctl-serve\n\n")
		fmt.Fprint(w, serve.SystemdUnit(spec))
		return nil
	}
	l, err := serve.NewLaunchd()
	if err != nil {
		return err
	}
	path, err := l.Install(spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Installed %s\nagentctl serve now runs at login and restarts if it exits.\n", path)
	fmt.Fprintf(w, "Logs: %s/%s\nNext: agentctl serve pair\n", dir, serve.LogFile)
	return nil
}

// RunServeUninstall removes the LaunchAgent.
func RunServeUninstall(w io.Writer) error {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(w, "Run: systemctl --user disable --now agentctl-serve && rm ~/.config/systemd/user/agentctl-serve.service")
		return nil
	}
	l, err := serve.NewLaunchd()
	if err != nil {
		return err
	}
	path, err := l.Uninstall()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Removed %s\n", path)
	return nil
}

// RunServeStatus prints whether the server runs and the state on disk.
func RunServeStatus(w io.Writer) error {
	dir, err := serve.Dir()
	if err != nil {
		return err
	}
	return printServeStatus(w, dir, time.Now())
}

func printServeStatus(w io.Writer, dir string, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	st, err := serve.QueryStatus(dir)
	switch {
	case err == nil:
		fmt.Fprintf(tw, "running:\tyes (pid %d)\n", st.PID)
		fmt.Fprintf(tw, "url:\t%s\n", st.URL)
		fmt.Fprintf(tw, "version:\t%s\n", st.Version)
		fmt.Fprintf(tw, "started:\t%s\n", st.Started.Local().Format(time.RFC1123))
		if st.Pairing {
			fmt.Fprintf(tw, "pairing:\topen\n")
		}
	case errors.Is(err, serve.ErrNotRunning):
		if pid, ok := pidfile.Running(dir, serve.PIDFile, "serve"); ok {
			fmt.Fprintf(tw, "running:\tpid %d, but the control socket does not answer\n", pid)
		} else {
			fmt.Fprintf(tw, "running:\tno\n")
		}
	default:
		fmt.Fprintf(tw, "running:\tunknown (%v)\n", err)
	}
	if store, err := serve.OpenDeviceStore(dir); err == nil {
		if list, err := store.List(); err == nil {
			fmt.Fprintf(tw, "devices:\t%d\n", len(list))
		}
	}
	if leaf, err := serve.ReadLeaf(dir); err == nil {
		days := int(leaf.NotAfter.Sub(now).Hours() / 24)
		fmt.Fprintf(tw, "cert expires:\t%s (%d days)\n", leaf.NotAfter.Local().Format("2006-01-02"), days)
	}
	if ca, err := serve.LoadCA(dir); err == nil {
		fmt.Fprintf(tw, "CA fingerprint:\t%s\n", ca.Fingerprint())
		if ca.Constrained() {
			fmt.Fprintf(tw, "CA constraints:\tlocal names only\n")
		} else {
			fmt.Fprintf(tw, "CA constraints:\tnone (run: agentctl serve ca rotate)\n")
		}
	} else {
		fmt.Fprintf(tw, "CA:\tnot created yet (agentctl serve creates it on first start)\n")
	}
	fmt.Fprintf(tw, "state dir:\t%s\n", dir)
	return tw.Flush()
}

// RunServePair asks the running server to open a pairing window and prints
// the two QR codes.
func RunServePair(w io.Writer, name string) error {
	dir, err := serve.Dir()
	if err != nil {
		return err
	}
	info, err := serve.RequestPairing(dir, name)
	if err != nil {
		return err
	}
	printPairing(w, info)
	return nil
}

func printPairing(w io.Writer, info serve.PairInfo) {
	fmt.Fprintf(w, "Pairing open until %s (5 minutes, one device).\n\n", info.Expires.Local().Format("15:04:05"))
	fmt.Fprintf(w, "CA SHA-256 fingerprint (check it in iOS Settings > General > About > Certificate Trust Settings):\n  %s\n\n", info.Fingerprint)
	if info.BootstrapURL != "" {
		fmt.Fprintf(w, "Step 1: install the CA (skip if this phone already trusts it)\n  %s\n", info.BootstrapURL)
		qrterminal.GenerateHalfBlock(info.BootstrapURL, qrterminal.L, w)
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Step 2: pair this device\n  %s\n", info.PairURL)
	qrterminal.GenerateHalfBlock(info.PairURL, qrterminal.L, w)
}

// RunServeDevices lists paired devices.
func RunServeDevices(w io.Writer) error {
	dir, err := serve.Dir()
	if err != nil {
		return err
	}
	store, err := serve.OpenDeviceStore(dir)
	if err != nil {
		return err
	}
	list, err := store.List()
	if err != nil {
		return err
	}
	printDevices(w, list)
	return nil
}

func printDevices(w io.Writer, list []serve.Device) {
	if len(list) == 0 {
		fmt.Fprintln(w, "No paired devices. Run: agentctl serve pair")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPAIRED\tLAST SEEN")
	for _, d := range list {
		seen := "never"
		if !d.LastSeen.IsZero() {
			seen = d.LastSeen.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Created.Local().Format("2006-01-02 15:04"), seen)
	}
	_ = tw.Flush()
}

// RunServeDevicesRm revokes a device by id or name. The running server
// re-reads devices.json, so the revocation applies to its next request.
func RunServeDevicesRm(w io.Writer, ref string) error {
	dir, err := serve.Dir()
	if err != nil {
		return err
	}
	store, err := serve.OpenDeviceStore(dir)
	if err != nil {
		return err
	}
	d, err := store.Remove(ref)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Revoked %s (%s)\n", d.ID, d.Name)
	return nil
}

// RunServeCARotate replaces the CA (and with it the server certificate and
// every paired device). A running server is told to restart so it creates
// and serves the new CA immediately.
func RunServeCARotate(w io.Writer) error {
	dir, err := serveDir()
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	return rotateCA(w, dir, hostname, config.Load().Serve.Hostnames, time.Now())
}

func rotateCA(w io.Writer, dir, hostname string, hostnames []string, now time.Time) error {
	old := ""
	if ca, err := serve.LoadCA(dir); err == nil {
		old = ca.Fingerprint()
	}
	_, statusErr := serve.QueryStatus(dir)
	running := statusErr == nil
	ca, err := serve.RotateCA(dir, hostname, hostnames, !running, now)
	if err != nil {
		return err
	}
	if old != "" {
		fmt.Fprintf(w, "Old CA fingerprint: %s\n", old)
	}
	if running {
		if err := serve.RequestServerRestart(dir); err != nil {
			return fmt.Errorf("CA removed, but asking the server to restart failed (restart it yourself): %w", err)
		}
		ca = waitForCA(dir, 10*time.Second)
	}
	if ca != nil {
		fmt.Fprintf(w, "New CA fingerprint: %s\n", ca.Fingerprint())
	} else {
		fmt.Fprintln(w, "The new CA is created when agentctl serve next starts.")
	}
	fmt.Fprintln(w, "All paired devices were revoked. On each phone: delete the old agentctl profile (Settings > General > VPN & Device Management), then run `agentctl serve pair` and install the new profile.")
	return nil
}

// waitForCA polls for the CA the restarting server creates.
func waitForCA(dir string, timeout time.Duration) *serve.CA {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ca, err := serve.LoadCA(dir); err == nil {
			return ca
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}
