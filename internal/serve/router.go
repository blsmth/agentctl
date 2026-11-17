package serve

import (
	"net/http"
	"slices"
	"time"
)

// Router registers handlers at one of three access levels. Every request has
// already passed the source-CIDR filter and, for non-GET methods, the CSRF
// header check (see Server.chain) before it reaches a handler.
//
// Later PRs add a file per area (routes_read.go, routes_write.go, ...) with a
// method such as
//
//	func (s *Server) registerRead(rt *Router) {
//		rt.Feature("fleet")
//		rt.Device("GET /v1/fleet", s.handleFleet)
//	}
//
// and one line in Server.routes calling it.
type Router struct {
	mux        *http.ServeMux
	auth       deviceAuthenticator
	allowWrite bool
	features   []string
	now        func() time.Time
}

func newRouter(auth deviceAuthenticator, allowWrite bool, now func() time.Time) *Router {
	return &Router{mux: http.NewServeMux(), auth: auth, allowWrite: allowWrite, now: now}
}

// Public registers a handler that needs no device cookie. Use it only for
// what an unpaired phone must reach: /healthz, /pair, and assets the
// "not paired" page or the manifest needs.
func (rt *Router) Public(pattern string, h http.HandlerFunc) {
	rt.mux.Handle(pattern, h)
}

// Device registers a handler that requires a paired device. The device is
// available to the handler through DeviceFrom.
func (rt *Router) Device(pattern string, h http.HandlerFunc) {
	rt.mux.Handle(pattern, requireDevice(rt.auth, rt.now, h))
}

// Write registers a state-changing handler: it requires a paired device and
// serve.allow_write. Register write routes with a non-GET method so the CSRF
// check applies.
func (rt *Router) Write(pattern string, h http.HandlerFunc) {
	rt.mux.Handle(pattern, requireDevice(rt.auth, rt.now, requireWrite(rt.allowWrite, h)))
}

// Feature advertises a capability in /healthz "features" so the web app can
// feature-detect endpoints that later PRs add.
func (rt *Router) Feature(name string) {
	if !slices.Contains(rt.features, name) {
		rt.features = append(rt.features, name)
	}
}

// Features returns the advertised features, never nil.
func (rt *Router) Features() []string {
	out := make([]string, len(rt.features))
	copy(out, rt.features)
	return out
}

// ServeHTTP dispatches to the registered handlers.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mux.ServeHTTP(w, r)
}
