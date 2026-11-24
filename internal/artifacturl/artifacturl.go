// Package artifacturl builds the browser URLs of published artifacts and
// opens them. It is shared by `agentctl artifact` and the hub, which cannot
// import internal/cmd (cmd imports the hub).
package artifacturl

import (
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/servectl"
)

// Direct is the content URL: <base>/a/<id>/latest/, or <base>/a/<id>/<rev>/
// for a numbered revision. The id is the credential, so it opens in any
// browser on an allowed network without pairing (a legacy 10-character id
// still needs a paired browser).
func Direct(base, id string, rev int) string {
	r := "latest"
	if rev > 0 {
		r = strconv.Itoa(rev)
	}
	return strings.TrimRight(base, "/") + "/a/" + id + "/" + r + "/"
}

// Serve builds the base URL of the PWA from the serve config and reports
// whether serve is running (a control-socket probe, no network). With
// serve.public_hostname set and a valid public certificate held by the
// running server, the base is the server's public URL instead.
func Serve() (string, bool) {
	cfg := config.Load().Serve
	running := false
	dir, dirErr := servectl.Dir()
	if dirErr == nil {
		// Phone access off still serves artifact links.
		running = servectl.Probe(dir).Serving()
	}
	host, _ := os.Hostname()
	return chooseBase(cfg, host, running, func() (string, error) { return queryPublicURL(dir) }), running
}

// AppBase is the base URL of phone-app links (#/artifacts/...): always the
// local host from config, never the public hostname, because the app needs
// the device cookie set at pairing on the local host.
func AppBase() string {
	host, _ := os.Hostname()
	return BaseURL(config.Load().Serve, host)
}

// queryPublicURL is servectl.QueryPublicURL; a var so tests can stub it.
var queryPublicURL = servectl.QueryPublicURL

// chooseBase is BaseURL, or the running server's public URL when
// serve.public_hostname is set and the server reports one. publicURL is
// called only then, so an unset key costs nothing.
func chooseBase(cfg config.ServeConfig, osHostname string, running bool, publicURL func() (string, error)) string {
	if running && strings.TrimSpace(cfg.PublicHostname) != "" {
		if u, err := publicURL(); err == nil && u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return BaseURL(cfg, osHostname)
}

// BaseURL is https://<host>[:<port>] from serve.listen and the first
// serve.hostnames entry, else <hostname>.local.
func BaseURL(cfg config.ServeConfig, osHostname string) string {
	lh, port, err := net.SplitHostPort(cfg.EffectiveListen())
	if err != nil {
		lh, port = "", strings.TrimPrefix(cfg.EffectiveListen(), ":")
	}
	host := ""
	switch {
	case len(cfg.Hostnames) > 0:
		host = cfg.Hostnames[0]
	case lh != "" && !net.ParseIP(lh).IsUnspecified():
		host = lh
	default:
		h := strings.TrimSuffix(osHostname, ".local")
		if h == "" {
			h = "localhost"
		}
		host = h + ".local"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	u := url.URL{Scheme: "https", Host: host}
	if port != "" && port != "443" {
		u.Host = host + ":" + port
	}
	return u.String()
}

// OpenInBrowser opens u with the platform opener without waiting for it.
func OpenInBrowser(u string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	c := exec.Command(name, u)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
