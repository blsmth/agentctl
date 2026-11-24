package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

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

// defaultLaptopIdleSeconds is serve.push.laptop_idle_seconds when unset.
const defaultLaptopIdleSeconds = 120

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
	// PublicHostname, when set, makes serve obtain a publicly trusted
	// wildcard certificate for its parent (*.<name minus first label>) from
	// Let's Encrypt (Cloudflare DNS-01) and advertise this name once held.
	// Empty disables it. Read it through EffectivePublicHostname, which
	// lowercases and validates it.
	PublicHostname string `json:"public_hostname,omitempty"`
	// ACME configures the certificate request for PublicHostname.
	ACME ServeACMEConfig `json:"acme,omitzero"`
}

// ServeACMEConfig is the `serve.acme` block.
type ServeACMEConfig struct {
	// Email is the ACME account contact (expiry warnings). Optional.
	Email string `json:"email,omitempty"`
	// Staging uses the Let's Encrypt staging CA, whose certificates no
	// browser trusts. For testing the setup without production rate limits.
	Staging bool `json:"staging,omitempty"`
}

// EffectivePublicHostname returns serve.public_hostname lowercased, without
// surrounding space or a trailing dot. Empty means unset. A value that is
// not a plain DNS name (an IP, a wildcard, a single label, bad characters)
// is an error.
func (s ServeConfig) EffectivePublicHostname() (string, error) {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.PublicHostname)), ".")
	if h == "" {
		return "", nil
	}
	if err := validateDNSName(h); err != nil {
		return "", fmt.Errorf("serve.public_hostname %q: %w", s.PublicHostname, err)
	}
	return h, nil
}

// validateDNSName checks h (already lowercased) is a fully qualified host
// name: at least three labels of letters, digits and inner hyphens, each at
// most 63 bytes, at most 253 bytes in all, and not an IP address.
func validateDNSName(h string) error {
	if len(h) > 253 {
		return errors.New("longer than 253 characters")
	}
	if net.ParseIP(h) != nil {
		return errors.New("is an IP address, not a DNS name")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 3 {
		// The certificate is a wildcard for the parent name; with fewer
		// labels it would be *.com or *.example.com, far too broad.
		return errors.New("needs at least three labels, such as mac.agentctl.example.com (the certificate is a wildcard for its parent)")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return errors.New("has an empty or over-long label")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("label %q starts or ends with a hyphen", l)
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("label %q has invalid character %q", l, r)
			}
		}
	}
	if tld := labels[len(labels)-1]; strings.Trim(tld, "0123456789") == "" {
		return errors.New("top-level label is numeric")
	}
	return nil
}

// ServePushConfig is the `serve.push` block.
type ServePushConfig struct {
	// Blocked pushes when an agent becomes blocked. nil means enabled.
	Blocked *bool `json:"blocked,omitempty"`
	// Done pushes when an agent finishes. nil means enabled.
	Done *bool `json:"done,omitempty"`
	// Approvals pushes when an approval (dispatch or bash popup) waits for
	// an answer from the phone. nil means enabled. Quiet hours do not apply:
	// an unanswered approval blocks work and is denied at its deadline.
	Approvals *bool `json:"approvals,omitempty"`
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
	// SuppressWhileOpen skips web push to a device that has the app open in
	// the foreground. nil means enabled; ntfy is unaffected.
	SuppressWhileOpen *bool `json:"suppress_while_open,omitempty"`
	// MutedProjects are project names that never push.
	MutedProjects []string `json:"muted_projects,omitempty"`
	// Artifacts pushes when an agent publishes a new artifact (`agentctl
	// artifact add`, not updates). nil means enabled.
	Artifacts *bool `json:"artifacts,omitempty"`
	// QuietWhenLaptopActive holds every push (web push and ntfy) while
	// someone is using the laptop: macOS HID idle time, or input on an
	// agentctl tmux client where ioreg is unavailable, under
	// LaptopIdleSeconds. Approvals and still-blocked agents are pushed once
	// when the laptop goes idle. nil means enabled.
	QuietWhenLaptopActive *bool `json:"quiet_when_laptop_active,omitempty"`
	// LaptopIdleSeconds is how long without laptop input before pushes
	// resume. nil or <= 0 means 120.
	LaptopIdleSeconds *int `json:"laptop_idle_seconds,omitempty"`
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

// ArtifactsEnabled reports whether new-artifact pushes are on (default true).
func (p ServePushConfig) ArtifactsEnabled() bool {
	return p.Artifacts == nil || *p.Artifacts
}

// ApprovalsEnabled reports whether approval pushes are on (default true).
func (p ServePushConfig) ApprovalsEnabled() bool {
	return p.Approvals == nil || *p.Approvals
}

// SuppressWhileOpenEnabled reports whether pushes skip devices with the app
// in the foreground (default true).
func (p ServePushConfig) SuppressWhileOpenEnabled() bool {
	return p.SuppressWhileOpen == nil || *p.SuppressWhileOpen
}

// QuietWhenLaptopActiveEnabled reports whether pushes are held while the
// laptop is in use (default true).
func (p ServePushConfig) QuietWhenLaptopActiveEnabled() bool {
	return p.QuietWhenLaptopActive == nil || *p.QuietWhenLaptopActive
}

// EffectiveLaptopIdleSeconds returns the laptop idle threshold in seconds
// (default 120; unset, zero and negative values mean the default).
func (p ServePushConfig) EffectiveLaptopIdleSeconds() int {
	if p.LaptopIdleSeconds == nil || *p.LaptopIdleSeconds <= 0 {
		return defaultLaptopIdleSeconds
	}
	return *p.LaptopIdleSeconds
}

// EffectiveBlockedReminderMinutes returns the reminder delay in minutes
// (default 10; 0 means disabled; negative values are treated as 0).
func (p ServePushConfig) EffectiveBlockedReminderMinutes() int {
	if p.BlockedReminderMinutes == nil {
		return defaultBlockedReminderMinutes
	}
	return max(*p.BlockedReminderMinutes, 0)
}
