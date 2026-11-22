package serve

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// hostCacheTTL is how long a collected snapshot is served before the next
// request recollects, so any number of open phones cost one collection per
// interval. CPU percentages are deltas between consecutive collections.
const hostCacheTTL = 3 * time.Second

// HostSource collects machine and agentctl process statistics.
type HostSource interface {
	Collect(ctx context.Context) HostStats
}

// HostStats is the host-specific part of GET /v1/host. Fields a platform
// cannot provide are omitted.
type HostStats struct {
	System    HostSystem   `json:"host"`
	Processes ProcessStats `json:"processes"`
}

// HostSystem describes the machine.
type HostSystem struct {
	Hostname       string      `json:"hostname,omitempty"`
	OS             string      `json:"os,omitempty"`
	Arch           string      `json:"arch,omitempty"`
	UptimeSec      uint64      `json:"uptime_sec,omitempty"`
	Cores          int         `json:"cores,omitempty"`
	Load           *LoadAvg    `json:"load,omitempty"`
	CPUPercent     *float64    `json:"cpu_percent,omitempty"`
	Memory         *MemoryInfo `json:"memory,omitempty"`
	Swap           *SwapInfo   `json:"swap,omitempty"`
	MemoryPressure string      `json:"memory_pressure,omitempty"` // normal | warn | critical
}

// LoadAvg is the 1/5/15 minute load average.
type LoadAvg struct {
	L1  float64 `json:"l1"`
	L5  float64 `json:"l5"`
	L15 float64 `json:"l15"`
}

// MemoryInfo is physical memory in bytes.
type MemoryInfo struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
}

// SwapInfo is swap in bytes.
type SwapInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// ProcessStats summarises processes by kind plus the busiest few.
type ProcessStats struct {
	Groups []ProcessGroup `json:"groups"`
	Top    []ProcessInfo  `json:"top"`
}

// ProcessGroup is the count and summed CPU% of one kind of process
// ("claude", "agentctl chrome-bar", ...). CPU% is of one core, so 400 means
// four cores' worth.
type ProcessGroup struct {
	Kind       string  `json:"kind"`
	Count      int     `json:"count"`
	CPUPercent float64 `json:"cpu_percent"`
	RSS        uint64  `json:"rss"`
}

// ProcessInfo is one process.
type ProcessInfo struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind,omitempty"`
	PID        int32   `json:"pid"`
	CPUPercent float64 `json:"cpu_percent"`
	RSS        uint64  `json:"rss"`
}

type hostBody struct {
	ServerTime string `json:"server_time"`
	HostStats
	Agentctl hostAgentctl `json:"agentctl"`
}

type hostAgentctl struct {
	Version     string `json:"version"`
	UptimeSec   int64  `json:"uptime_sec"`
	TmuxWindows *int   `json:"tmux_windows,omitempty"`
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	st := s.host.Collect(r.Context())
	b := hostBody{
		ServerTime: s.now().UTC().Format(time.RFC3339Nano),
		HostStats:  st,
		Agentctl: hostAgentctl{
			Version:   s.opts.Version,
			UptimeSec: int64(s.now().Sub(s.started).Seconds()),
		},
	}
	if n, ok := s.tmuxWindowCount(); ok {
		b.Agentctl.TmuxWindows = &n
	}
	writeJSON(w, http.StatusOK, b)
}

// tmuxWindowCount counts windows with a short cache of its own (it spawns tmux).
func (s *Server) tmuxWindowCount() (int, bool) {
	s.hostWinMu.Lock()
	defer s.hostWinMu.Unlock()
	if !s.hostWinAt.IsZero() && s.now().Sub(s.hostWinAt) < hostCacheTTL {
		return s.hostWinN, s.hostWinOK
	}
	n, err := s.hostWin()
	s.hostWinN, s.hostWinOK, s.hostWinAt = n, err == nil, s.now()
	return s.hostWinN, s.hostWinOK
}

// liveHost is the production HostSource (gopsutil, no cgo, no subprocesses).
type liveHost struct {
	now func() time.Time
	mu  sync.Mutex

	at   time.Time
	last HostStats

	prevAt    time.Time
	prevCPU   *cpu.TimesStat
	prevProcs map[int32]float64 // pid -> cumulative CPU seconds
}

func newLiveHost(now func() time.Time) *liveHost { return &liveHost{now: now} }

// Collect returns the cached snapshot, recollecting when it is older than
// hostCacheTTL. Concurrent callers share one collection.
func (l *liveHost) Collect(ctx context.Context) HostStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.at.IsZero() && l.now().Sub(l.at) < hostCacheTTL {
		return l.last
	}
	l.last = l.collect(ctx)
	l.at = l.now()
	return l.last
}

func (l *liveHost) collect(ctx context.Context) HostStats {
	now := l.now()
	var st HostStats
	sys := &st.System
	sys.OS, sys.Arch = runtime.GOOS, runtime.GOARCH
	if hi, err := host.InfoWithContext(ctx); err == nil {
		sys.Hostname, sys.UptimeSec = hi.Hostname, hi.Uptime
		if hi.KernelArch != "" {
			sys.Arch = hi.KernelArch
		}
	} else if h, err := os.Hostname(); err == nil {
		sys.Hostname = h
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		sys.Cores = n
	}
	if la, err := load.AvgWithContext(ctx); err == nil {
		sys.Load = &LoadAvg{L1: la.Load1, L5: la.Load5, L15: la.Load15}
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		sys.Memory = &MemoryInfo{Total: vm.Total, Used: vm.Used, Available: vm.Available}
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		sys.Swap = &SwapInfo{Total: sw.Total, Used: sw.Used}
	}
	sys.MemoryPressure = memoryPressure()

	elapsed := now.Sub(l.prevAt).Seconds()
	haveDelta := !l.prevAt.IsZero() && elapsed > 0.5
	if ts, err := cpu.TimesWithContext(ctx, false); err == nil && len(ts) == 1 {
		if haveDelta && l.prevCPU != nil {
			if p := cpuBusyPercent(*l.prevCPU, ts[0]); p >= 0 {
				sys.CPUPercent = &p
			}
		}
		l.prevCPU = &ts[0]
	}
	st.Processes = l.collectProcesses(ctx, haveDelta, elapsed)
	l.prevAt = now
	return st
}

// cpuBusyPercent is the non-idle share of the time between two samples.
func cpuBusyPercent(a, b cpu.TimesStat) float64 {
	total := func(t cpu.TimesStat) float64 {
		return t.User + t.System + t.Nice + t.Idle + t.Iowait + t.Irq + t.Softirq + t.Steal
	}
	idle := func(t cpu.TimesStat) float64 { return t.Idle + t.Iowait }
	dt := total(b) - total(a)
	if dt <= 0 {
		return -1
	}
	p := (1 - (idle(b)-idle(a))/dt) * 100
	return min(max(p, 0), 100)
}

func (l *liveHost) collectProcesses(ctx context.Context, haveDelta bool, elapsed float64) ProcessStats {
	procs, _ := process.ProcessesWithContext(ctx)
	cur := make(map[int32]float64, len(procs))
	type entry struct {
		p    *process.Process
		name string
		kind string
		cpu  float64
	}
	var all []entry
	for _, p := range procs {
		ts, err := p.TimesWithContext(ctx)
		if err != nil {
			continue
		}
		secs := ts.User + ts.System
		cur[p.Pid] = secs
		var pct float64
		if prev, ok := l.prevProcs[p.Pid]; ok && haveDelta {
			pct = max(secs-prev, 0) / elapsed * 100
		}
		name, _ := p.NameWithContext(ctx)
		all = append(all, entry{p: p, name: name, cpu: pct})
	}
	l.prevProcs = cur

	groups := map[string]*ProcessGroup{}
	for i := range all {
		e := &all[i]
		base := strings.ToLower(e.name)
		var args []string
		if strings.HasPrefix(base, "agentctl") || versionLike(base) {
			args, _ = e.p.CmdlineSliceWithContext(ctx)
		}
		e.kind = classifyProcess(e.name, args)
		if e.kind == "" {
			continue
		}
		g := groups[e.kind]
		if g == nil {
			g = &ProcessGroup{Kind: e.kind}
			groups[e.kind] = g
		}
		g.Count++
		g.CPUPercent += e.cpu
		if mi, err := e.p.MemoryInfoWithContext(ctx); err == nil {
			g.RSS += mi.RSS
		}
	}
	var out ProcessStats
	out.Groups = []ProcessGroup{}
	for _, g := range groups {
		g.CPUPercent = round1(g.CPUPercent)
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		if out.Groups[i].CPUPercent != out.Groups[j].CPUPercent {
			return out.Groups[i].CPUPercent > out.Groups[j].CPUPercent
		}
		return out.Groups[i].Kind < out.Groups[j].Kind
	})
	sort.Slice(all, func(i, j int) bool { return all[i].cpu > all[j].cpu })
	out.Top = []ProcessInfo{}
	for _, e := range all {
		if len(out.Top) == 5 {
			break
		}
		pi := ProcessInfo{Name: e.name, Kind: e.kind, PID: e.p.Pid, CPUPercent: round1(e.cpu)}
		if mi, err := e.p.MemoryInfoWithContext(ctx); err == nil {
			pi.RSS = mi.RSS
		}
		out.Top = append(out.Top, pi)
	}
	return out
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// agentctlSubcommands are the subcommands the host view breaks out; any other
// agentctl process is "agentctl other".
var agentctlSubcommands = map[string]bool{
	"chrome-bar": true, "serve": true, "hub-view": true, "hud": true, "metrics": true,
}

// classifyProcess maps a process to a group kind, or "" when it is not one of
// ours. args is the full command line (only needed for agentctl).
func classifyProcess(name string, args []string) string {
	base := strings.ToLower(filepath.Base(name))
	switch {
	case base == "claude":
		return "claude"
	case versionLike(base) && len(args) > 0 && strings.Contains(strings.ToLower(args[0]), "claude"):
		// The native Claude Code binary reports its version as its name.
		return "claude"
	case base == "agentctl":
		for _, a := range args[min(1, len(args)):] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if agentctlSubcommands[a] {
				return "agentctl " + a
			}
			break
		}
		return "agentctl other"
	}
	return ""
}

// versionLike reports whether s looks like "2.1.45".
func versionLike(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' || !strings.Contains(s, ".") {
		return false
	}
	return strings.Trim(s, "0123456789.") == ""
}
