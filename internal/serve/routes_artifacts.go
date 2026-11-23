package serve

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
)

// The artifacts API: list and read the agent-published artifacts of
// internal/artifacts, pin or delete them, and serve their content under
// /a/<id>/<rev>/<path> in a sandboxed, opaque origin (artifact_content.go).
// `agentctl artifact` is the only writer of content; the phone can only pin
// and delete.

// artifactRevView is one revision with the URL of its content.
type artifactRevView struct {
	artifacts.Revision
	RawURL string `json:"raw_url"`
}

// artifactView is an artifact as /v1/artifacts serves it: its metadata, the
// viewer route (url) and the capability URL of the latest revision's entry
// (raw_url), minted for the requesting device; each revision has its own.
type artifactView struct {
	artifacts.Meta
	Revs     []artifactRevView `json:"revs"`
	Latest   int               `json:"latest"`
	RevCount int               `json:"rev_count"`
	URL      string            `json:"url"`
	RawURL   string            `json:"raw_url"`
}

// artifactViewerURL is the web app route of an artifact.
func artifactViewerURL(id string) string { return "/#/artifacts/" + url.PathEscape(id) }

// viewOfArtifact builds the view with capability URLs for device.
func (s *Server) viewOfArtifact(m artifacts.Meta, device string) artifactView {
	v := artifactView{
		Meta: m, Latest: m.Latest(), RevCount: len(m.Revs),
		URL:  artifactViewerURL(m.ID),
		Revs: make([]artifactRevView, 0, len(m.Revs)),
	}
	for _, r := range m.Revs {
		u := artifactTokenURL(s.mintArtifactToken(m.ID, r.N, device), m.ID, r.N, "")
		v.Revs = append(v.Revs, artifactRevView{Revision: r, RawURL: u})
		if r.N == v.Latest {
			v.RawURL = u
		}
	}
	return v
}

func (s *Server) registerArtifacts(rt *Router) {
	rt.Feature("artifacts")
	rt.Device("GET /v1/artifacts", s.handleArtifacts)
	rt.Device("GET /v1/artifacts/{id}", s.handleArtifact)
	rt.Write("DELETE /v1/artifacts/{id}", writeRoute(s, "artifact-delete", s.handleArtifactDelete))
	rt.Write("POST /v1/artifacts/{id}/pin", writeRoute(s, "artifact-pin", s.handleArtifactPin))
	// The cookie authenticates only the entry request, which redirects to the
	// capability URL; subresources of the sandboxed document carry no cookie.
	rt.Device("GET /a/{id}/{rev}/{path...}", s.handleArtifactEntry)
	rt.Device("GET /a/{id}/{rev}", s.handleArtifactRevRoot)
	rt.Public("GET /a/t/{token}/{id}/{rev}/{path...}", s.handleArtifactContent)
	rt.Public("GET /a/t/{token}/{id}/{rev}", s.handleArtifactTokenRoot)
}

// artifactActionFor names an artifact write route by path, for auditing
// requests rejected before routing.
func artifactActionFor(path string) (string, bool) {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) < 3 || p[0] != "v1" || p[1] != "artifacts" {
		return "", false
	}
	switch {
	case len(p) == 3:
		return "artifact-delete", true
	case len(p) == 4 && p[3] == "pin":
		return "artifact-pin", true
	}
	return "", false
}

// parseSinceParam accepts an RFC 3339 time or a duration back from now
// ("24h", "7d"). Empty is the zero time (no filter).
func parseSinceParam(v string, now time.Time) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	d, err := sessions.ParseSince(v)
	if err != nil {
		return time.Time{}, errors.New("since must be an RFC 3339 time or a duration like 24h or 7d")
	}
	if d <= 0 {
		return time.Time{}, nil
	}
	return now.Add(-d), nil
}

// handleArtifacts is GET /v1/artifacts?project=&since=: newest first.
func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, err := parseSinceParam(q.Get("since"), s.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	list, err := s.artifacts.List(artifacts.Filter{Project: q.Get("project"), Since: since})
	if err != nil {
		logging.Warn("serve: listing artifacts: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "listing artifacts failed")
		return
	}
	d, _ := DeviceFrom(r.Context())
	out := make([]artifactView, 0, len(list))
	for _, m := range list {
		out = append(out, s.viewOfArtifact(m, d.ID))
	}
	writeJSON(w, http.StatusOK, struct {
		Artifacts []artifactView `json:"artifacts"`
	}{out})
}

// handleArtifact is GET /v1/artifacts/{id}.
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	m, err := s.artifacts.Get(r.PathValue("id"))
	if err != nil {
		status, code, msg := artifactErr(err)
		writeError(w, status, code, msg)
		return
	}
	d, _ := DeviceFrom(r.Context())
	writeJSON(w, http.StatusOK, s.viewOfArtifact(m, d.ID))
}

// artifactErr maps a store error to a status, code and client message.
func artifactErr(err error) (int, string, string) {
	switch {
	case errors.Is(err, artifacts.ErrNotFound):
		return http.StatusNotFound, codeNotFound, "no such artifact"
	case errors.Is(err, artifacts.ErrBadPath):
		return http.StatusBadRequest, codeBadRequest, "invalid artifact path"
	}
	logging.Warn("serve: artifact: %v", err)
	return http.StatusInternalServerError, codeInternal, "reading the artifact failed"
}

// artifactPinIn is the body of POST /v1/artifacts/{id}/pin.
type artifactPinIn struct {
	Pinned *bool `json:"pinned"`
}

func (s *Server) handleArtifactPin(r *http.Request, in artifactPinIn, a *auditEntry) outcome {
	id := r.PathValue("id")
	a.Artifact = clip(id)
	if in.Pinned == nil {
		return failure(http.StatusBadRequest, codeBadRequest, `body must be {"pinned": true|false}`)
	}
	m, err := s.artifacts.Get(id)
	if err != nil {
		status, code, msg := artifactErr(err)
		return failure(status, code, msg)
	}
	a.Project = clip(m.Project)
	a.Agent = clip(m.Agent)
	s.auditAttempt(*a)
	m, err = s.artifacts.Pin(id, *in.Pinned)
	if err != nil {
		status, code, msg := artifactErr(err)
		return failure(status, code, msg)
	}
	s.artifactWatch.Kick()
	d, _ := DeviceFrom(r.Context())
	return success(s.viewOfArtifact(m, d.ID))
}

func (s *Server) handleArtifactDelete(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	id := r.PathValue("id")
	a.Artifact = clip(id)
	m, err := s.artifacts.Get(id)
	if err != nil {
		status, code, msg := artifactErr(err)
		return failure(status, code, msg)
	}
	a.Project = clip(m.Project)
	a.Agent = clip(m.Agent)
	s.auditAttempt(*a)
	if _, err := s.artifacts.Remove(id); err != nil {
		status, code, msg := artifactErr(err)
		return failure(status, code, msg)
	}
	s.artifactWatch.Kick()
	return success(okBody)
}
