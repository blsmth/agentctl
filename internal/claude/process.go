package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// startProcess spawns a Claude CLI subprocess configured for stream-json I/O.
// It waits for the {"type":"system","subtype":"init"} message before returning,
// confirming the process is ready to accept prompts.
//
// Returns the cmd, a WriteCloser for stdin, a Scanner over stdout, and the
// session ID from the init message.
func startProcess(model string, newCmdFn func(string) *exec.Cmd) (
	cmd *exec.Cmd, stdin io.WriteCloser, stdout *bufio.Scanner, sessionID string, err error,
) {
	cmd = newCmdFn(model)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, "", fmt.Errorf("start process: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	// Wait for the system/init line.
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Type == "system" && msg.Subtype == "init" {
			return cmd, stdinPipe, scanner, msg.SessionID, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, nil, "", fmt.Errorf("reading init message: %w", err)
	}

	// Process exited before sending init.
	_ = cmd.Wait()
	return nil, nil, nil, "", fmt.Errorf("subprocess exited before sending init message")
}

// defaultNewCmd returns an exec.Cmd for the Claude CLI with stream-json I/O.
// The env is cleaned to strip AGENTCTL vars and prevent hook recursion.
func defaultNewCmd(model string) *exec.Cmd {
	cmd := exec.Command("claude", "-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--model", model,
		"--verbose",
	)
	cmd.Env = cleanEnv()
	return cmd
}

// cleanEnv returns a minimal environment for the subprocess. Critically, it
// excludes AGENTCTL=1 and all AGENTCTL_* vars so that Claude Code's hook
// handlers see guardAgentctl() == false and do not recurse into work-journal
// or other agentctl-managed operations.
func cleanEnv() []string {
	kept := []string{"HOME", "PATH", "USER", "SHELL", "TERM", "LANG", "LC_ALL"}
	env := make([]string, 0, len(kept))
	for _, k := range kept {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	// Also pass through any ANTHROPIC_* vars (API key, base URL overrides)
	// in case the user has API key auth configured.
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "ANTHROPIC_") {
			env = append(env, e)
		}
	}
	return env
}
