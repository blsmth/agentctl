package mailbox

// Read returns windowID's already-delivered messages in chronological order.
// Test-only: production code never reads delivered mail back.
func Read(windowID string) ([]Message, error) {
	return listMessages(readDir(windowID))
}
