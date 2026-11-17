package serve

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"

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
	s.sender = NewSender(s.devices, subs, pusher)
	if topic := opts.Config.NtfyTopic; topic != "" {
		s.ntfy = &Ntfy{
			Base:   opts.NtfyBase,
			Topic:  topic,
			AppURL: "https://" + net.JoinHostPort(opts.AdvertiseHost, strconv.Itoa(opts.HTTPSPort)),
		}
	}
	s.pushQ = newPushDispatcher(s.sender, s.ntfy)
	s.notifier = NewNotifier(opts.Config.Push, s.now, s.pushQ.enqueue)
	return nil
}

// registerPush registers the push endpoints.
func (s *Server) registerPush(rt *Router) {
	rt.Feature("push")
	rt.Device("GET /v1/push/vapid", s.handlePushVAPID)
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
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	switch {
	case errors.Is(err, errNoSubscription):
		writeError(w, http.StatusConflict, codeBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, codeInternal, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
