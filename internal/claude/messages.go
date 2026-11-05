package claude

// contentBlock is a typed content block in an assistant message body.
// Used when parsing stream-json assistant messages.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
