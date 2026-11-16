package backendauth

import "net/http"

// Version is the agentctl version used in the User-Agent; main sets it at
// startup. Nous sits behind Cloudflare, which blocks Go's default
// User-Agent with a 403 HTML page.
var Version = "dev"

// UserAgent returns the User-Agent sent on every request to a backend.
func UserAgent() string { return "agentctl/" + Version }

// SetUserAgent sets the agentctl User-Agent on req. Every request to Nous
// goes through it.
func SetUserAgent(req *http.Request) { req.Header.Set("User-Agent", UserAgent()) }
