package statusline

import (
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/phoneview"
	"github.com/blsmth/agentctl/internal/tmux"
)

// clientListTimeout bounds the `tmux list-clients` call made when the phone is
// watching the window; like the git dirty count it must not stall a refresh.
const clientListTimeout = 150 * time.Millisecond

// Seams, replaced in tests so nothing touches real tmux or the real state dir.
var (
	phoneViewDirFn = func() string {
		d, err := phoneview.DefaultDir()
		if err != nil {
			return ""
		}
		return d
	}
	listClientsFn = tmux.ListClientsTimeout
)

// effectiveFormat is the format to render for windowID. It is the configured
// statusline_format unless the phone has a live terminal open on the window
// and no laptop tmux client is looking at it, in which case it is
// statusline_mobile_format. The common case costs one directory read; tmux is
// queried only when a phone marker exists, and any failure keeps the normal
// format.
func effectiveFormat(cfg *config.Config, windowID string) string {
	normal := cfg.EffectiveStatuslineFormat()
	mobile := cfg.EffectiveStatuslineMobileFormat()
	if mobile == normal || windowID == "" {
		return normal
	}
	if !phoneview.Active(phoneViewDirFn(), windowID) {
		return normal
	}
	clients, err := listClientsFn(clientListTimeout)
	if err != nil {
		logging.Debug("statusline: listing tmux clients: %v", err)
		return normal
	}
	if tmux.LaptopViewing(clients, windowID) {
		return normal
	}
	return mobile
}
