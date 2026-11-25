package artifactpdf

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/config"
)

// ConfigKey names the global config key that overrides browser discovery.
const ConfigKey = "artifacts.pdf_browser"

// appBinaries are the macOS app bundle executables tried first, in order.
var appBinaries = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
}

// pathBinaries are the command names tried on PATH after the app bundles.
var pathBinaries = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}

// Seams for tests.
var (
	statFile = os.Stat
	lookPath = defaultLookPath
	// configured returns artifacts.pdf_browser from the global config; a
	// project-local layer cannot set it (config sanitizes it away).
	configured = func() string { return config.Load().Artifacts.PDFBrowser }
)

var defaultLookPath = exec.LookPath

// ErrNoBrowser is returned (wrapped) when no Chrome, Chromium or Edge is found.
var ErrNoBrowser = errors.New("no Chrome, Chromium or Edge found")

// ErrBadBrowserConfig is matched (errors.Is) by the error FindBrowser returns
// when artifacts.pdf_browser is set to something that is not an executable.
var ErrBadBrowserConfig = errors.New(ConfigKey + " is not an executable file")

// badConfigError names the configured path; it Is ErrBadBrowserConfig.
type badConfigError struct{ path string }

func (e badConfigError) Error() string {
	return fmt.Sprintf("%s %q is not an executable file", ConfigKey, e.path)
}

func (e badConfigError) Is(target error) bool { return target == ErrBadBrowserConfig }

// FindBrowser returns the browser binary to render with: the configured
// artifacts.pdf_browser when set (it must be an executable file), else the
// first installed of the usual locations.
func FindBrowser() (string, error) {
	if p := configured(); p != "" {
		if !isExecutable(p) {
			return "", badConfigError{path: p}
		}
		return p, nil
	}
	for _, p := range appBinaries {
		if isExecutable(p) {
			return p, nil
		}
	}
	for _, name := range pathBinaries {
		if p, err := lookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: install Google Chrome or set %s in the global config to a browser binary (e.g. %s)",
		ErrNoBrowser, ConfigKey, filepath.Join("/Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"))
}

func isExecutable(p string) bool {
	fi, err := statFile(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}
