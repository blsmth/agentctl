package serve

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/safefs"
)

// Artifact content is reached through a capability in the path,
// /a/t/<token>/<id>/<rev>/<path>, not through the device cookie. Browsers do
// not send the SameSite=Strict cookie on the subresource requests of a
// sandboxed (opaque-origin) document, nor on those of a top-level artifact
// page whose initiator is opaque, so a cookie-only /a/ breaks every relative
// stylesheet, script and image. With the token in the base path the
// document's relative URLs carry it automatically.
//
// A token is scoped to one artifact, one resolved revision number (never
// "latest") and one paired device, and expires. It is
// <expiry base36>.<device id>.<HMAC-SHA256 truncated, base64url> over all of
// them with a per-machine key in the serve dir. A revoked device's tokens
// stop working at once (the device must still be paired).

const (
	// ArtifactKeyFile holds the artifact URL signing key (0600).
	ArtifactKeyFile = "artifact-url.key"
	artifactKeyLen  = 32
	// artifactTokenTTL is how long a minted URL stays valid. Expiries are
	// rounded up to the hour so the URLs a viewer gets within one hour are
	// identical (an iframe does not reload for nothing).
	artifactTokenTTL = 24 * time.Hour
	artifactMACLen   = 18
)

var artifactTokenRe = regexp.MustCompile(`^([0-9a-z]{1,13})\.([0-9a-f]{12})\.([A-Za-z0-9_-]{24})$`)

// loadOrCreateArtifactKey reads the signing key, creating it on first use.
func loadOrCreateArtifactKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, ArtifactKeyFile)
	data, err := readFileOrEmpty(path)
	if err != nil {
		return nil, err
	}
	if len(data) == artifactKeyLen {
		return data, nil
	}
	if len(data) != 0 {
		return nil, fmt.Errorf("%s is %d bytes, want %d (delete it to regenerate; open artifact links stop working)", path, len(data), artifactKeyLen)
	}
	key := make([]byte, artifactKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating artifact key: %w", err)
	}
	if err := safefs.WriteFile(dir, path, key, 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return key, nil
}

func artifactMAC(key []byte, id string, rev int, exp int64, device string) []byte {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "agentctl-artifact\x00%s\x00%d\x00%d\x00%s", id, rev, exp, device)
	return m.Sum(nil)[:artifactMACLen]
}

// mintArtifactToken returns a token for revision rev (> 0) of id, for device.
func (s *Server) mintArtifactToken(id string, rev int, device string) string {
	exp := s.now().Add(artifactTokenTTL).Truncate(time.Hour).Add(time.Hour).Unix()
	mac := artifactMAC(s.artifactKey, id, rev, exp, device)
	return strconv.FormatInt(exp, 36) + "." + device + "." + base64.RawURLEncoding.EncodeToString(mac)
}

// checkArtifactToken reports whether tok grants revision rev of id now.
func (s *Server) checkArtifactToken(tok, id string, rev int) bool {
	m := artifactTokenRe.FindStringSubmatch(tok)
	if m == nil || rev <= 0 {
		return false
	}
	exp, err := strconv.ParseInt(m[1], 36, 64)
	if err != nil || !s.now().Before(time.Unix(exp, 0)) {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(m[3])
	if err != nil || !hmac.Equal(got, artifactMAC(s.artifactKey, id, rev, exp, m[2])) {
		return false
	}
	return s.devicePaired(m[2])
}

// devicePaired reports whether a device id is still paired.
func (s *Server) devicePaired(id string) bool {
	list, err := s.devices.List()
	if err != nil {
		return false
	}
	for _, d := range list {
		if d.ID == id {
			return true
		}
	}
	return false
}

// artifactTokenURL is the capability URL of a file of a revision.
func artifactTokenURL(tok, id string, rev int, rel string) string {
	return "/a/t/" + tok + "/" + id + "/" + strconv.Itoa(rev) + "/" + escapeRel(rel)
}

// isArtifactTokenPath reports a /a/t/ path (CORS-readable, see
// setArtifactHeaders).
func isArtifactTokenPath(p string) bool { return strings.HasPrefix(p, "/a/t/") }
