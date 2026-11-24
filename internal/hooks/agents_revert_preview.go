package hooks

import (
	"os"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tui"
)

// `agentctl agents revert` rewrites an agent definition from the hidden
// history (internal/agenthistory), so from an agent session it always goes
// through an approval popup, like `agentctl template add|edit|rm`
// (templatePreviewGate). `history` and `diff` are read-only and not gated. A
// human in a plain shell never meets this: it is a Claude Code hook.

// gateAgentsRevert is the denial-wording kind for this gate. The popup itself
// is the generic command popup (PreviewKindBash): the command line is the
// whole story (name and revision).
const gateAgentsRevert PreviewKind = "agents-revert"

// agentsRevertGate is the seam ValidateBash uses to show the popup.
var agentsRevertGate = agentsRevertPreviewGate

// agentsRevertSegments returns every `agentctl agents revert ...`
// invocation in command (quoted mentions and data heredocs never match).
func agentsRevertSegments(command string) []cronSegment {
	tokens := invocationTokens(command)
	var segs []cronSegment
	for i := 0; i+2 < len(tokens); i++ {
		if !strings.HasSuffix(tokens[i].text, "agentctl") {
			continue
		}
		if g := tokens[i+1].text; g != "agents" && g != "agent-manager" {
			continue
		}
		sub := strings.TrimRight(tokens[i+2].text, ";|&")
		if sub != "revert" {
			continue
		}
		if sub != tokens[i+2].text {
			segs = append(segs, cronSegment{sub: sub})
			continue
		}
		segs = append(segs, cronSegment{sub: sub, tokens: takeSegment(tokens[i+3:])})
	}
	return segs
}

// isAgentctlAgentsRevert reports whether command runs `agentctl agents
// revert` (other than asking for its help).
func isAgentctlAgentsRevert(command string) bool {
	for _, s := range agentsRevertSegments(command) {
		if !segmentHasHelpFlags(s.tokens, func(string) bool { return false }) {
			return true
		}
	}
	return false
}

// agentsRevertPreviewGate shows the approval popup for a gated revert. Its
// contract is templatePreviewGate's.
func agentsRevertPreviewGate(agentName, command, cwd string) (approved, decided bool, denyReason string) {
	if os.Getenv("TMUX") == "" {
		logging.Log("hook-validate-bash: not in tmux session, passing through agents revert")
		return false, false, ""
	}
	release, ok := acquireGateLock(command)
	if !ok {
		logging.Log("hook-validate-bash: agents revert gate already owned by another invocation, staying silent")
		return false, false, ""
	}
	defer release()

	contentWidth, vpHeight := dispatchPreviewFit()
	target := resolvePopupTarget()
	args := DispatchPreviewArgs{
		AgentName:    target.label(agentName),
		Command:      command,
		Kind:         PreviewKindBash,
		ContentWidth: contentWidth,
		VPHeight:     vpHeight,
	}
	tmpPath, err := writePreviewArgs("agentctl-agents-revert-preview-*", args)
	if err != nil {
		logging.Log("hook-validate-bash: agents revert preview args: %v", err)
		return false, false, ""
	}
	defer os.Remove(tmpPath)

	sz := clampPopupToClient(tui.DispatchPreviewMaxPopupSize(
		tui.WithContentWidth(contentWidth),
		tui.WithMaxViewportHeight(vpHeight),
	))
	logging.Log("hook-validate-bash: launching agents revert popup agent=%s cmd=%s", agentName, textutil.Truncate(command, 100, "..."))
	return runPopupWithDeadline(target, "agent revert", "hook-validate-bash: agents revert", sz.Width, sz.Height, tmpPath, popupNotice{
		Agent:  agentName,
		Tool:   "Bash",
		Detail: textutil.Truncate(command, 100, "..."),
		Action: "revert an agent definition",
		Remote: &remoteDetails{Kind: remoteapproval.KindBash, AskingAgent: agentName, Command: command},
	})
}
