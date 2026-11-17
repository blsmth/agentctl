package config

// DefaultServeListen is the address `agentctl serve` binds when serve.listen
// is unset. Binding every interface survives DHCP and network changes;
// serve.allow_cidrs and device auth are the gate.
const DefaultServeListen = ":8443"

// DefaultServeAllowCIDRs is the source-address allowlist used when
// serve.allow_cidrs is unset: RFC 1918, CGNAT/Tailscale (100.64.0.0/10) and
// loopback.
var DefaultServeAllowCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"::1/128",
}

// defaultBlockedReminderMinutes is serve.push.blocked_reminder_minutes when
// unset.
const defaultBlockedReminderMinutes = 10

// ServeConfig is the `serve` block: settings for `agentctl serve`. Every key
// is global-only (see sanitizeProjectConfig). Read the Effective* helpers
// rather than the raw fields so defaults stay in one place.
type ServeConfig struct {
	// Listen is the HTTPS listen address. Default DefaultServeListen. The
	// plain-HTTP pairing bootstrap listener uses the same host and port+1.
	Listen string `json:"listen,omitempty"`
	// Hostnames are extra DNS SANs for the server certificate.
	// <hostname>.local and every non-loopback interface IP are always added.
	Hostnames []string `json:"hostnames,omitempty"`
	// AllowCIDRs is the source-address allowlist. A request from outside it
	// gets 403 before anything else runs, /healthz included. Default
	// DefaultServeAllowCIDRs.
	AllowCIDRs []string `json:"allow_cidrs,omitempty"`
	// AllowWrite enables the write endpoints (answer, reply, interrupt,
	// dispatch, close). They return 403 unless true.
	AllowWrite bool `json:"allow_write,omitempty"`
	// KeepAwake holds a `caffeinate -s` child while any agent is working or
	// blocked. nil (unset) means enabled; use KeepAwakeEnabled.
	KeepAwake *bool `json:"keep_awake,omitempty"`
	// Push configures Web Push / ntfy notification triggers.
	Push ServePushConfig `json:"push,omitzero"`
	// NtfyTopic, when set, also posts notifications to https://ntfy.sh/<topic>.
	// ntfy.sh topics are public: anyone who knows the name can subscribe, so
	// it must be long and random.
	NtfyTopic string `json:"ntfy_topic,omitempty"`
	// NtfyDetails puts agent and project names, and a Click link with the
	// server's address, into ntfy posts. Off by default: the topic is public.
	NtfyDetails bool `json:"ntfy_details,omitempty"`
}

// ServePushConfig is the `serve.push` block.
type ServePushConfig struct {
	// Blocked pushes when an agent becomes blocked. nil means enabled.
	Blocked *bool `json:"blocked,omitempty"`
	// Done pushes when an agent finishes. nil means enabled.
	Done *bool `json:"done,omitempty"`
	// BlockedReminderMinutes sends one reminder per blocked episode after
	// this many minutes. nil means 10; 0 disables reminders.
	BlockedReminderMinutes *int `json:"blocked_reminder_minutes,omitempty"`
	// QuietHours is a local-time "HH:MM-HH:MM" range with no pushes. Empty
	// means none.
	QuietHours string `json:"quiet_hours,omitempty"`
	// VAPIDSubscriber is the contact (mailto: or https: URL) sent to push
	// services as the VAPID subject. Empty means
	// https://github.com/blsmth/agentctl.
	VAPIDSubscriber string `json:"vapid_subscriber,omitempty"`
	// MutedProjects are project names that never push.
	MutedProjects []string `json:"muted_projects,omitempty"`
}

// EffectiveListen returns Listen or DefaultServeListen.
func (s ServeConfig) EffectiveListen() string {
	if s.Listen == "" {
		return DefaultServeListen
	}
	return s.Listen
}

// EffectiveAllowCIDRs returns AllowCIDRs or a copy of DefaultServeAllowCIDRs.
func (s ServeConfig) EffectiveAllowCIDRs() []string {
	if len(s.AllowCIDRs) == 0 {
		return append([]string(nil), DefaultServeAllowCIDRs...)
	}
	return s.AllowCIDRs
}

// KeepAwakeEnabled reports whether keep-awake is on (default true).
func (s ServeConfig) KeepAwakeEnabled() bool {
	return s.KeepAwake == nil || *s.KeepAwake
}

// BlockedEnabled reports whether blocked pushes are on (default true).
func (p ServePushConfig) BlockedEnabled() bool {
	return p.Blocked == nil || *p.Blocked
}

// DoneEnabled reports whether done pushes are on (default true).
func (p ServePushConfig) DoneEnabled() bool {
	return p.Done == nil || *p.Done
}

// EffectiveBlockedReminderMinutes returns the reminder delay in minutes
// (default 10; 0 means disabled; negative values are treated as 0).
func (p ServePushConfig) EffectiveBlockedReminderMinutes() int {
	if p.BlockedReminderMinutes == nil {
		return defaultBlockedReminderMinutes
	}
	return max(*p.BlockedReminderMinutes, 0)
}
