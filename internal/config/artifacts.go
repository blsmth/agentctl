package config

// ArtifactsConfig configures artifact export. Global config only: PDFBrowser
// is a binary that agentctl executes, so a cloned repo must not set it.
type ArtifactsConfig struct {
	// PDFBrowser is the path of the Chrome/Chromium/Edge binary that renders
	// `agentctl artifact pdf`. Empty means discover one on this machine.
	PDFBrowser string `json:"pdf_browser,omitempty"`
}
