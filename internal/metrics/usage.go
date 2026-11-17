package metrics

import (
	"github.com/blsmth/agentctl/internal/logstream"
)

// UsageSet tracks per-window log streams feeding a Collector: one persistent
// logstream.Stream per window, with the token/tool/turn collector and the cost
// estimator subscribed, labelled with the window's project and agent. It is
// the usage half of the metrics serve (it used to live in the per-project
// monitor). Not safe for concurrent use.
type UsageSet struct {
	collector *Collector
	streams   map[string]*usageStream
}

type usageStream struct {
	stream  *logstream.Stream
	path    string
	project string
	agent   string
}

// NewUsageSet returns an empty set reporting into c.
func NewUsageSet(c *Collector) *UsageSet {
	return &UsageSet{collector: c, streams: make(map[string]*usageStream)}
}

// Attach points key's stream at path, creating it on first use and switching
// it when the path changed (e.g. after /clear). A change of project or agent
// rebuilds the stream so series carry the right labels. An empty path drops
// the stream.
func (u *UsageSet) Attach(key, project, agent, path string) {
	if path == "" {
		u.Drop(key)
		return
	}
	cur, ok := u.streams[key]
	if ok && (cur.project != project || cur.agent != agent) {
		u.Drop(key)
		ok = false
	}
	if !ok {
		s := logstream.NewStream(path)
		s.Subscribe(u.collector.ForProjectAgent(project, agent))
		s.Subscribe(u.collector.NewProjectCostEstimator(project, agent))
		u.streams[key] = &usageStream{stream: s, path: path, project: project, agent: agent}
		return
	}
	if cur.path != path {
		cur.path = path
		cur.stream.SetPath(path)
	}
}

// Drop closes and forgets key's stream.
func (u *UsageSet) Drop(key string) {
	if cur, ok := u.streams[key]; ok {
		cur.stream.Close()
		delete(u.streams, key)
	}
}

// Retain drops every stream whose key is not in keep.
func (u *UsageSet) Retain(keep map[string]bool) {
	for k := range u.streams {
		if !keep[k] {
			u.Drop(k)
		}
	}
}

// Tick reads new log entries on every stream.
func (u *UsageSet) Tick() {
	for _, cur := range u.streams {
		cur.stream.Tick()
	}
}

// Len reports how many streams are tracked.
func (u *UsageSet) Len() int { return len(u.streams) }
