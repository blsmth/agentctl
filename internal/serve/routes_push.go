package serve

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// initPush loads the VAPID keys and subscription store and builds the
// notifier. It runs in New, so a broken vapid.json fails startup loudly.
func (s *Server) initPush(opts Options) error {
	keys, err := LoadOrCreateVAPID(opts.Dir)
	if err != nil {
		return err
	}
	subs, err := OpenSubscriptionStore(opts.Dir)
	if err != nil {
		return err
	}
	s.vapid, s.subs = keys, subs
	pusher := opts.Pusher
	if pusher == nil {
		pusher = newWebPusher(keys, opts.Config.Push.VAPIDSubscriber)
	}
	s.presence = NewPresence(s.now)
	s.sender = NewSender(s.devices, subs, pusher)
	if opts.Config.Push.SuppressWhileOpenEnabled() {
		s.sender.SetPresence(s.presence)
	}
	if topic := opts.Config.NtfyTopic; topic != "" {
		s.ntfy = &Ntfy{
			Base:    opts.NtfyBase,
			Topic:   topic,
			Details: opts.Config.NtfyDetails,
			AppURL:  "https://" + net.JoinHostPort(opts.AdvertiseHost, strconv.Itoa(opts.HTTPSPort)),
		}
	}
	s.sender.SetMuted(s.phone.Off)
	s.pushQ = newPushDispatcher(s.sender, s.ntfy)
	s.pushQ.muted = s.phone.Off
	s.notifier = NewNotifier(opts.Config.Push, s.now, s.pushQ.enqueue)
	if opts.Config.Push.QuietWhenLaptopActiveEnabled() {
		probe := opts.LaptopIdle
		if probe == nil {
			probe = laptopIdleProbe
		}
		idle := time.Duration(opts.Config.Push.EffectiveLaptopIdleSeconds()) * time.Second
		laptop := newLaptopActivity(probe, idle, s.now)
		s.sender.SetLaptop(laptop)
		s.notifier.SetLaptop(laptop, s.approvalPending)
	}
	return nil
}

// registerPush registers the push endpoints.
func (s *Server) registerPush(rt *Router) {
	rt.Feature("push")
	rt.Device("GET /v1/push/vapid", s.handlePushVAPID)
	rt.Device("GET /v1/push/subscription", s.handlePushSubscription)
	rt.Device("POST /v1/push/subscribe", s.handlePushSubscribe)
	rt.Device("DELETE /v1/push/subscribe", s.handlePushUnsubscribe)
	rt.Device("POST /v1/push/test", s.handlePushTest)
}

func (s *Server) handlePushVAPID(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"public_key": s.vapid.Public})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no device")
		return
	}
	var sub Subscription
	r.Body = http.MaxBytesReader(w, r.Body, maxSubscriptionBytes)
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid subscription JSON")
		return
	}
	if err := sub.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if err := s.subs.Set(dev.ID, sub, s.now()); err != nil {
		logging.Warn("serve: storing subscription: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "storing subscription")
		return
	}
	logging.Log("serve: push: device %s (%s) subscribed via %s", dev.ID, dev.Name, endpointHost(sub.Endpoint))
	s.auditPush(r, dev, "push_subscribe", "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// auditPush records a push-management action by dev.
func (s *Server) auditPush(r *http.Request, dev Device, action, result string) {
	e := s.auditFor(r, dev, action)
	e.Result = result
	s.recordAudit(e)
}

// handlePushSubscription reports whether the calling device has a stored
// subscription.
func (s *Server) handlePushSubscription(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no device")
		return
	}
	_, has := s.subs.Get(dev.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": has})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no device")
		return
	}
	if err := s.subs.Delete(dev.ID); err != nil {
		logging.Warn("serve: removing subscription: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "removing subscription")
		return
	}
	logging.Log("serve: push: device %s (%s) unsubscribed", dev.ID, dev.Name)
	s.auditPush(r, dev, "push_unsubscribe", "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePushTest sends a test push to the calling device only.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no device")
		return
	}
	err := s.sender.SendTo(r.Context(), dev.ID, Message{
		Title: "agentctl: test notification",
		Body:  "Push is working for " + dev.Name,
		URL:   "/#/settings",
		Tag:   "agentctl:test",
		Kind:  "test",
	})
	s.auditPush(r, dev, "push_test", testResult(err))
	switch {
	case errors.Is(err, errNoSubscription), errors.Is(err, errSubscriptionExpired):
		writeError(w, http.StatusConflict, codeBadRequest, err.Error())
	case err != nil:
		// Never echo the delivery error: it carries dial errors and status
		// codes. deliver logged the detail.
		logging.Warn("serve: test push to device %s: %v", dev.ID, err)
		writeError(w, http.StatusBadGateway, codeInternal, "push delivery failed; see serve.log")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func testResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, errNoSubscription):
		return "no_subscription"
	case errors.Is(err, errSubscriptionExpired):
		return "expired"
	default:
		return "failed"
	}
}
