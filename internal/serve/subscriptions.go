package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/safefs"
)

// SubscriptionsFile holds Web Push subscriptions, keyed by device id.
const SubscriptionsFile = "subscriptions.json"

// maxSubscriptionBytes bounds a posted PushSubscription.
const maxSubscriptionBytes = 8 << 10

// Subscription is a browser PushSubscription (the JSON the web app posts) plus
// when it was stored. A device has at most one.
type Subscription struct {
	Endpoint string    `json:"endpoint"`
	Keys     SubKeys   `json:"keys"`
	Created  time.Time `json:"created,omitzero"`
}

// SubKeys are the subscription's encryption keys.
type SubKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// Validate checks that s is usable. The endpoint must be https: the server
// POSTs to it, so a plain-http or odd-scheme URL is refused.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("endpoint must be an https URL")
	}
	if s.Keys.P256dh == "" || s.Keys.Auth == "" {
		return errors.New("keys.p256dh and keys.auth are required")
	}
	return nil
}

// SubscriptionStore persists subscriptions to subscriptions.json using the
// same flock + atomic replace pattern as DeviceStore, since the CLI (device
// revocation) and the server both write it.
type SubscriptionStore struct {
	path string

	mu    sync.Mutex
	subs  map[string]Subscription
	stamp fileStamp
}

// OpenSubscriptionStore returns the store for <dir>/subscriptions.json. A
// missing file is an empty store.
func OpenSubscriptionStore(dir string) (*SubscriptionStore, error) {
	s := &SubscriptionStore{path: filepath.Join(dir, SubscriptionsFile)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// All returns a copy of every subscription, keyed by device id.
func (s *SubscriptionStore) All() (map[string]Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	out := make(map[string]Subscription, len(s.subs))
	for k, v := range s.subs {
		out[k] = v
	}
	return out, nil
}

// Get returns the device's subscription.
func (s *SubscriptionStore) Get(deviceID string) (Subscription, bool) {
	all, err := s.All()
	if err != nil {
		return Subscription{}, false
	}
	sub, ok := all[deviceID]
	return sub, ok
}

// Set stores (replacing) the device's subscription.
func (s *SubscriptionStore) Set(deviceID string, sub Subscription, now time.Time) error {
	sub.Created = now.UTC()
	return s.mutate(func(m map[string]Subscription) { m[deviceID] = sub })
}

// Delete drops the subscriptions of the given devices.
func (s *SubscriptionStore) Delete(deviceIDs ...string) error {
	return s.mutate(func(m map[string]Subscription) {
		for _, id := range deviceIDs {
			delete(m, id)
		}
	})
}

// DeleteEndpoint drops every subscription with the endpoint (an expired one).
func (s *SubscriptionStore) DeleteEndpoint(endpoint string) error {
	return s.mutate(func(m map[string]Subscription) {
		for id, sub := range m {
			if sub.Endpoint == endpoint {
				delete(m, id)
			}
		}
	})
}

// Keep drops every subscription whose device id is not in keep.
func (s *SubscriptionStore) Keep(keep map[string]bool) error {
	return s.mutate(func(m map[string]Subscription) {
		for id := range m {
			if !keep[id] {
				delete(m, id)
			}
		}
	})
}

func (s *SubscriptionStore) mutate(fn func(map[string]Subscription)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	m, err := readSubscriptions(s.path)
	if err != nil {
		return err
	}
	fn(m)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding subscriptions: %w", err)
	}
	if err := safefs.WriteFile(filepath.Dir(s.path), s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", s.path, err)
	}
	s.subs = m
	s.stamp = statFile(s.path)
	return nil
}

func (s *SubscriptionStore) reloadLocked() error {
	st := statFile(s.path)
	if s.subs != nil && st.same(s.stamp) {
		return nil
	}
	m, err := readSubscriptions(s.path)
	if err != nil {
		return err
	}
	s.subs, s.stamp = m, st
	return nil
}

func readSubscriptions(path string) (map[string]Subscription, error) {
	m := map[string]Subscription{}
	data, err := readFileOrEmpty(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m == nil {
		m = map[string]Subscription{}
	}
	return m, nil
}

// dropSubscriptions removes the devices' subscriptions from <dir>. Called when
// a device is revoked; failures are returned for the caller to log, because a
// leftover subscription is also filtered at send time.
func dropSubscriptions(dir string, deviceIDs ...string) error {
	store, err := OpenSubscriptionStore(dir)
	if err != nil {
		return err
	}
	return store.Delete(deviceIDs...)
}

// clearSubscriptions removes every subscription from <dir>.
func clearSubscriptions(dir string) error {
	store, err := OpenSubscriptionStore(dir)
	if err != nil {
		return err
	}
	return store.Keep(nil)
}
