package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/safefs"
)

// VAPIDFile holds the server's VAPID keypair under the serve dir.
const VAPIDFile = "vapid.json"

// DefaultVAPIDSubscriber is the VAPID "sub" claim: a contact the push service
// can reach. Apple rejects a JWT whose subject is not a usable mailto: or
// https: URL (BadJwtToken), so the default is the project's URL, not a
// localhost address.
const DefaultVAPIDSubscriber = "https://github.com/blsmth/agentctl"

const (
	// pushTTL is how long a push service keeps an undelivered message.
	pushTTL = 3600 // seconds
	// pushTimeout bounds one Web Push or ntfy request.
	pushTimeout = 10 * time.Second
)

// VAPIDKeys is the keypair stored in vapid.json.
type VAPIDKeys struct {
	Private string `json:"private_key"`
	Public  string `json:"public_key"`
}

// LoadOrCreateVAPID reads <dir>/vapid.json, generating it (0600) on first run.
func LoadOrCreateVAPID(dir string) (VAPIDKeys, error) {
	path := filepath.Join(dir, VAPIDFile)
	data, err := readFileOrEmpty(path)
	if err != nil {
		return VAPIDKeys{}, err
	}
	if len(data) > 0 {
		var k VAPIDKeys
		if err := json.Unmarshal(data, &k); err != nil {
			return VAPIDKeys{}, fmt.Errorf("parsing %s: %w", path, err)
		}
		if k.Private != "" && k.Public != "" {
			return k, nil
		}
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("generating VAPID keys: %w", err)
	}
	k := VAPIDKeys{Private: priv, Public: pub}
	out, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("encoding VAPID keys: %w", err)
	}
	if err := safefs.WriteFile(dir, path, append(out, '\n'), 0o600); err != nil {
		return VAPIDKeys{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return k, nil
}

// PushOptions are the per-message delivery options.
type PushOptions struct {
	// Urgent sends Urgency: high (used for blocked agents).
	Urgent bool
	// TTL is how long (seconds) the push service keeps an undelivered
	// message; 0 means pushTTL.
	TTL int
}

// Pusher delivers one encrypted Web Push message. It returns the push
// service's HTTP status; a non-nil error means the request itself failed.
type Pusher interface {
	Push(ctx context.Context, sub Subscription, payload []byte, opts PushOptions) (status int, err error)
}

// webPusher is the production Pusher over webpush-go.
type webPusher struct {
	keys       VAPIDKeys
	subscriber string
	client     *http.Client
}

func newWebPusher(keys VAPIDKeys, subscriber string) *webPusher {
	return &webPusher{keys: keys, subscriber: vapidSubscriber(subscriber), client: newPushClient()}
}

// vapidSubscriber returns the value to hand webpush-go as Options.Subscriber.
// webpush-go (v1.4) prepends "mailto:" to anything that does not start with
// "https:", so a configured "mailto:x@y" would become "mailto:mailto:x@y";
// the scheme is stripped here and webpush-go adds it back exactly once.
func vapidSubscriber(configured string) string {
	s := strings.TrimSpace(configured)
	if s == "" {
		s = DefaultVAPIDSubscriber
	}
	if len(s) >= len("mailto:") && strings.EqualFold(s[:len("mailto:")], "mailto:") {
		s = s[len("mailto:"):]
	}
	return s
}

func (p *webPusher) Push(ctx context.Context, sub Subscription, payload []byte, opts PushOptions) (int, error) {
	ttl := pushTTL
	if opts.TTL > 0 {
		ttl = opts.TTL
	}
	urgency := webpush.UrgencyNormal
	if opts.Urgent {
		urgency = webpush.UrgencyHigh
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth},
	}, &webpush.Options{
		HTTPClient:      p.client,
		Subscriber:      p.subscriber,
		VAPIDPublicKey:  p.keys.Public,
		VAPIDPrivateKey: p.keys.Private,
		TTL:             ttl,
		Urgency:         urgency,
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode, nil
}

// Message is one notification. It never carries pane or prompt text.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// URL is the app-relative route to open, "/#/w/<session>/<window>".
	URL string `json:"url"`
	// Tag collapses notifications for the same window: "<session>:<window>".
	Tag string `json:"tag"`

	// Urgent, Kind and TTL are not part of the payload.
	Urgent bool   `json:"-"`
	Kind   string `json:"-"` // "blocked", "reminder", "done", "approval", "test"
	TTL    int    `json:"-"` // seconds; 0 means pushTTL
}

func (m Message) payload() []byte {
	b, _ := json.Marshal(m)
	return b
}

// encodeURIComponent mirrors the JavaScript function of the same name, which
// is what the web app's router uses to build and decode hash routes.
func encodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("-_.!~*'()", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// windowURL is the app route for a window, as the web app's router parses it.
func windowURL(session, windowID string) string {
	return "/#/w/" + encodeURIComponent(session) + "/" + encodeURIComponent(windowID)
}

// Sender delivers messages to subscribed devices over Web Push. Devices that
// were revoked are skipped (and their subscriptions dropped) by checking the
// device store on every send, so a stale subscriptions.json never notifies a
// revoked phone.
type Sender struct {
	devices *DeviceStore
	subs    *SubscriptionStore
	pusher  Pusher
	// presence, when set, makes Broadcast skip devices with the app open.
	presence *Presence

	mu   sync.Mutex
	last *LastPush
}

// LastPush describes the most recent notification handed to Broadcast.
type LastPush struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Result string    `json:"result"`
}

// pushLogf writes the Info-level push diagnostics. It is a variable so tests
// can capture the lines.
var pushLogf = logging.Log

// LastPush returns the most recent Broadcast outcome, or nil.
func (s *Sender) LastPush() *LastPush {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil
	}
	c := *s.last
	return &c
}

func (s *Sender) record(kind, result string) {
	s.mu.Lock()
	s.last = &LastPush{Time: time.Now(), Kind: kind, Result: result}
	s.mu.Unlock()
}

// endpointHost is the push service host of a subscription endpoint, the only
// part of it that is safe to log.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return "unknown"
	}
	return u.Hostname()
}

// NewSender returns a Sender.
func NewSender(devices *DeviceStore, subs *SubscriptionStore, pusher Pusher) *Sender {
	return &Sender{devices: devices, subs: subs, pusher: pusher}
}

// SetPresence makes Broadcast skip devices that have the app in the
// foreground. SendTo is never suppressed.
func (s *Sender) SetPresence(p *Presence) { s.presence = p }

// Broadcast sends msg to every paired device with a subscription, except
// devices with the app open in the foreground.
func (s *Sender) Broadcast(ctx context.Context, msg Message) {
	live, err := s.liveDevices()
	if err != nil {
		logging.Warn("serve: push: %v", err)
		return
	}
	all, err := s.subs.All()
	if err != nil {
		logging.Warn("serve: push: reading subscriptions: %v", err)
		return
	}
	var stale []string
	targets := make(map[string]Subscription, len(all))
	for id, sub := range all {
		if !live[id] {
			stale = append(stale, id)
			if s.presence != nil {
				s.presence.Forget(id)
			}
			continue
		}
		targets[id] = sub
	}
	inApp := 0
	if s.presence != nil {
		for id := range targets {
			if s.presence.Foreground(id) {
				delete(targets, id)
				inApp++
			}
		}
	}
	switch {
	case len(targets) == 0 && inApp > 0:
		pushLogf("push: kind=%s tag=%s suppressed, %d device(s) in app", msg.Kind, msg.Tag, inApp)
		s.record(msg.Kind, fmt.Sprintf("sent to 0/0, %d in app", inApp))
	case len(targets) == 0:
		pushLogf("push: no subscribed devices, nothing sent (kind %s)", msg.Kind)
		s.record(msg.Kind, "no subscribed devices")
	default:
		pushLogf("push: kind=%s tag=%s subscriptions=%d in_app=%d", msg.Kind, msg.Tag, len(targets), inApp)
		sent := 0
		for id, sub := range targets {
			if s.deliver(ctx, id, sub, msg) == nil { // failures are logged in deliver
				sent++
			}
		}
		result := fmt.Sprintf("sent to %d/%d", sent, len(targets))
		if inApp > 0 {
			result += fmt.Sprintf(", %d in app", inApp)
		}
		s.record(msg.Kind, result)
	}
	if len(stale) > 0 {
		if err := s.subs.Delete(stale...); err != nil {
			logging.Warn("serve: push: dropping subscriptions of revoked devices: %v", err)
		}
	}
}

// SendTo sends msg to one device. It reports whether the device has a
// subscription that is still valid.
func (s *Sender) SendTo(ctx context.Context, deviceID string, msg Message) error {
	live, err := s.liveDevices()
	if err != nil {
		return err
	}
	if !live[deviceID] {
		return errors.New("device is not paired")
	}
	sub, ok := s.subs.Get(deviceID)
	if !ok {
		return errNoSubscription
	}
	return s.deliver(ctx, deviceID, sub, msg)
}

var (
	errNoSubscription = errors.New("this device has no push subscription")
	// errSubscriptionExpired: the push service said the subscription is
	// gone (404/410) and it was deleted.
	errSubscriptionExpired = errors.New("the push subscription expired and was removed")
	// errPushFailed is what callers see for any other delivery failure; the
	// detail (dial errors, status codes) goes only to the log, so a client
	// cannot use push delivery to probe the network.
	errPushFailed = errors.New("push delivery failed")
)

func (s *Sender) liveDevices() (map[string]bool, error) {
	devs, err := s.devices.List()
	if err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	live := make(map[string]bool, len(devs))
	for _, d := range devs {
		live[d.ID] = true
	}
	return live, nil
}

// deliver pushes to one subscription and deletes it when the push service says
// it is gone (404/410).
func (s *Sender) deliver(ctx context.Context, deviceID string, sub Subscription, msg Message) error {
	status, err := s.pusher.Push(ctx, sub, msg.payload(), PushOptions{Urgent: msg.Urgent, TTL: msg.TTL})
	switch {
	case err != nil:
		logging.Warn("serve: push to device %s failed: %v", deviceID, err)
		pushLogf("push: kind=%s device=%s result=failed (request error)", msg.Kind, deviceID)
		return errPushFailed
	case status == http.StatusNotFound || status == http.StatusGone:
		if derr := s.subs.DeleteEndpoint(sub.Endpoint); derr != nil {
			logging.Warn("serve: push: deleting expired subscription: %v", derr)
		}
		pushLogf("push: kind=%s device=%s host=%s result=expired-deleted (status %d)", msg.Kind, deviceID, endpointHost(sub.Endpoint), status)
		return errSubscriptionExpired
	case status < 200 || status >= 300:
		logging.Warn("serve: push to device %s: push service returned %d", deviceID, status)
		pushLogf("push: kind=%s device=%s host=%s result=failed (status %d)", msg.Kind, deviceID, endpointHost(sub.Endpoint), status)
		return errPushFailed
	}
	pushLogf("push: kind=%s device=%s host=%s result=sent (status %d)", msg.Kind, deviceID, endpointHost(sub.Endpoint), status)
	return nil
}

// ntfyBase is where the ntfy fallback posts.
const ntfyBase = "https://ntfy.sh"

// Ntfy posts notifications to an ntfy topic. Topics on ntfy.sh are public:
// anyone who knows or guesses the name can read them. So unless Details is
// set, a post carries only the kind of event: no agent or project name, no
// window, and no Click URL (which would publish the server's LAN address).
type Ntfy struct {
	Base   string // default ntfyBase
	Topic  string
	AppURL string // https://<host>:<port>, for the Click header (Details only)
	// Details posts the full title and body and a Click link to the window
	// (serve.ntfy_details).
	Details bool
}

// Send posts msg. It is bounded by pushTimeout; callers run it off the bus
// goroutine.
func (n *Ntfy) Send(ctx context.Context, msg Message) error {
	base := n.Base
	if base == "" {
		base = ntfyBase
	}
	client := &http.Client{Timeout: pushTimeout}
	if !n.Details {
		msg = redactForNtfy(msg)
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+url.PathEscape(n.Topic), strings.NewReader(msg.Body))
	if err != nil {
		return fmt.Errorf("building ntfy request: %w", err)
	}
	req.Header.Set("Title", msg.Title)
	if n.Details {
		req.Header.Set("Click", n.AppURL+msg.URL)
	}
	req.Header.Set("Tags", ntfyTags(msg.Kind))
	if msg.Urgent {
		req.Header.Set("Priority", "high")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to ntfy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %d", resp.StatusCode)
	}
	return nil
}

// redactForNtfy replaces a message's title and body with text that names
// only the kind of event (see Ntfy).
func redactForNtfy(m Message) Message {
	switch m.Kind {
	case "blocked":
		m.Title = "agentctl: an agent is blocked"
	case "reminder":
		m.Title = "agentctl: an agent is still blocked"
	case "done":
		m.Title = "agentctl: an agent finished"
	case "approval":
		m.Title = "agentctl: approval needed"
	default:
		m.Title = "agentctl: notification"
	}
	m.Body = "Open agentctl for details."
	m.URL = ""
	return m
}

func ntfyTags(kind string) string {
	switch kind {
	case "blocked":
		return "warning,agentctl"
	case "reminder":
		return "hourglass,agentctl"
	case "done":
		return "white_check_mark,agentctl"
	case "approval":
		return "raising_hand,agentctl"
	default:
		return "bell,agentctl"
	}
}
