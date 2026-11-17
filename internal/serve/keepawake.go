package serve

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

// Holder keeps the machine awake while held. Start and Stop are idempotent.
type Holder interface {
	Start() error
	Stop()
}

// caffeinate holds `caffeinate -s` (no system sleep on AC power) as a child.
// The child also gets `-w <our pid>` so it exits on its own if we crash; the
// pid survives selfexec, so Stop must still run before re-exec (Server.Close).
type caffeinate struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

// newHolder returns the platform Holder: caffeinate on darwin, a no-op
// elsewhere.
func newHolder() Holder {
	if runtime.GOOS != "darwin" {
		return noopHolder{}
	}
	return &caffeinate{}
}

type noopHolder struct{}

func (noopHolder) Start() error { return nil }
func (noopHolder) Stop()        {}

func (c *caffeinate) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return nil
	}
	cmd := exec.Command("caffeinate", "-s", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd = cmd
	go func() { _ = cmd.Wait() }() // reap if it exits on its own
	return nil
}

func (c *caffeinate) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	c.cmd = nil
}

// keepAwake drives a Holder from the fleet: held while any agent is working
// or blocked, released when the fleet is all idle/done and on Close.
type keepAwake struct {
	holder Holder
	held   bool
}

// set applies the desired state, logging holder failures once per change.
func (k *keepAwake) set(active bool) {
	if active == k.held {
		return
	}
	if active {
		if err := k.holder.Start(); err != nil {
			logging.Warn("serve: keep-awake: %v", err)
			return
		}
		logging.Log("serve: keep-awake on")
	} else {
		k.holder.Stop()
		logging.Log("serve: keep-awake off")
	}
	k.held = active
}

// runKeepAwake follows the bus until ctx is done, then releases the holder.
func runKeepAwake(ctx context.Context, bus *fleetwatch.Bus, h Holder) {
	k := &keepAwake{holder: h}
	defer k.set(false)
	snap, sub := bus.Subscribe()
	defer sub.Close()
	k.set(fleetwatch.Active(snap))
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.C:
			k.set(fleetwatch.Active(bus.Snapshot()))
		}
	}
}
