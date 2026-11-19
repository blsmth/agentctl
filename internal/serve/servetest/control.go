package servetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/blsmth/agentctl/internal/serve"
)

// registerControl mounts the control API. Routes:
//
//	GET  /__test/session   {cookie, token}: the paired device's cookie
//	POST /__test/reset     default fixture, no actions, no approvals
//	GET  /__test/actions   {actions: [...]} recorded since the last reset
//	GET  /__test/fleet     {groups: [...]} the current fixture
//	PUT  /__test/fleet     {groups: [...]} or {fixture: "<name>"}: swap fixtures
//	POST /__test/window    {session, id, ...fields}: patch one window
//	POST /__test/approvals ApprovalSpec -> {id}: open a remote approval
//
// Fixture changes reach the browser the way real ones do: the fleet bus
// re-reads the source (every Options.Rediscover) and publishes SSE events.
func (h *Harness) registerControl(mux *http.ServeMux) {
	p := ControlPrefix
	mux.HandleFunc("GET "+p+"session", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"cookie": serve.DeviceCookie, "token": h.Token})
	})
	mux.HandleFunc("POST "+p+"reset", func(w http.ResponseWriter, _ *http.Request) {
		h.Reset()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET "+p+"actions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string][]Action{"actions": h.Actions()})
	})
	mux.HandleFunc("GET "+p+"fleet", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string][]Group{"groups": h.world.snapshot()})
	})
	mux.HandleFunc("PUT "+p+"fleet", h.handleSetFleet)
	mux.HandleFunc("POST "+p+"window", h.handlePatchWindow)
	mux.HandleFunc("POST "+p+"approvals", func(w http.ResponseWriter, r *http.Request) {
		var spec ApprovalSpec
		if !decode(w, r, &spec) {
			return
		}
		id, err := h.OpenApproval(spec)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	})
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no control route %s %s", r.Method, r.URL.Path))
	})
}

func (h *Harness) handleSetFleet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fixture string  `json:"fixture"`
		Groups  []Group `json:"groups"`
	}
	if !decode(w, r, &in) {
		return
	}
	groups := in.Groups
	if in.Fixture != "" {
		f, ok := Fixtures[in.Fixture]
		if !ok {
			names := make([]string, 0, len(Fixtures))
			for n := range Fixtures {
				names = append(names, n)
			}
			sort.Strings(names)
			writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown fixture %q (have %v)", in.Fixture, names))
			return
		}
		groups = f()
	}
	if err := h.SetFleet(groups); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// windowPatch carries the fields a test may change on one window; nil
// leaves a field alone.
type windowPatch struct {
	Session     string  `json:"session"`
	ID          string  `json:"id"`
	State       *string `json:"state"`
	Done        *bool   `json:"done"`
	Activity    *string `json:"activity"`
	Agent       *string `json:"agent"`
	Pane        *string `json:"pane"`
	PaneText    *string `json:"pane_text"`
	Recap       *string `json:"recap"`
	RecapSource *string `json:"recap_source"`
	ShortTitle  *string `json:"short_title"`
}

func (h *Harness) handlePatchWindow(w http.ResponseWriter, r *http.Request) {
	var p windowPatch
	if !decode(w, r, &p) {
		return
	}
	if p.State != nil {
		if err := validate([]Group{{Session: "x", Windows: []Window{{ID: "@1", State: *p.State}}}}); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	err := h.world.update(p.Session, p.ID, func(x *Window) {
		set(&x.State, p.State)
		set(&x.Done, p.Done)
		set(&x.Activity, p.Activity)
		set(&x.Agent, p.Agent)
		set(&x.Pane, p.Pane)
		set(&x.PaneText, p.PaneText)
		set(&x.Recap, p.Recap)
		set(&x.RecapSource, p.RecapSource)
		set(&x.ShortTitle, p.ShortTitle)
	})
	if errors.Is(err, errNoWindow) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decoding body: %w", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
