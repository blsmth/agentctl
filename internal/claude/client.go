// Package claude manages the Claude Code CLI as a persistent subprocess and
// speaks line-delimited JSON (stream-json) to it over stdin/stdout.
//
// This package is retained deliberately. It is not dead code and it is not
// selected by default: it is registered in internal/llm as provider "claude",
// which only takes effect when the llm.provider config value is set to
// "claude" explicitly (llm.New has no default provider; an empty value means disabled).
//
// Why keep it: there is no official Claude Agent SDK for Go. The community Go
// ports all work the same way this package does -- spawn the CLI and talk
// NDJSON over stdio -- so this is already a minimal Agent SDK port, and
// deleting it would discard the plumbing any future Go agent integration
// needs. Its tests (client_test.go) keep it from rotting.
//
// Known constraint: that mechanism wraps `claude -p`-style subprocess usage,
// which is currently disallowed for agentctl's own background calls. The
// official anthropic-sdk-go is different: an API-key-authenticated HTTP client
// for the Messages API that shares no code with this package. If an SDK
// integration happens later it will most likely be a new llm.Completer built
// on anthropic-sdk-go, alongside "ollama" and "claude" rather than replacing
// either.
package claude

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/blsmth/agentctl/internal/logging"
)

const defaultModel = "haiku"

// Client wraps a persistent Claude CLI subprocess for stateless completions.
// A single subprocess is kept alive and reused across calls; it communicates
// over NDJSON on stdin/stdout using --input-format/--output-format stream-json.
//
// All exported methods are safe for concurrent use — they serialise via a mutex.
type Client struct {
	mu            sync.Mutex
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        *bufio.Scanner
	model         string
	lastRateLimit *RateLimitInfo
	ready         bool

	// newCmdFn is injectable so tests can substitute a mock subprocess.
	newCmdFn func(string) *exec.Cmd
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// NewClient spawns the Claude CLI subprocess and waits for the init message.
// Returns an error if the subprocess cannot be started or does not send an
// init message within its startup sequence.
func NewClient(opts ...ClientOption) (*Client, error) {
	c := &Client{
		model:    defaultModel,
		newCmdFn: defaultNewCmd,
	}
	for _, opt := range opts {
		opt(c)
	}
	if err := c.start(); err != nil {
		return nil, err
	}
	return c, nil
}

// start (re)starts the subprocess. Caller must hold mu or be in a
// single-threaded context (e.g. NewClient before the client is shared).
func (c *Client) start() error {
	cmd, stdin, stdout, sessionID, err := startProcess(c.model, c.newCmdFn)
	if err != nil {
		return fmt.Errorf("start claude subprocess: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.ready = true
	logging.Log("claude.Client: subprocess started session_id=%s model=%s pid=%d", sessionID, c.model, cmd.Process.Pid)
	return nil
}

// Complete sends a single-turn prompt to the subprocess and returns the text
// response. Transparently restarts the subprocess on a single failure.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	return c.CompleteWithSystem(ctx, "", prompt)
}

// CompleteWithSystem sends a prompt with an optional system instruction.
// When system is non-empty, it is prepended to the user prompt so that the
// persistent subprocess does not need to be restarted between calls.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string) (string, error) {
	done := logging.Timing("claude.Complete")
	defer done()

	fullPrompt := prompt
	if system != "" {
		fullPrompt = "System: " + system + "\n\n" + prompt
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.doComplete(ctx, fullPrompt)
	if err != nil {
		// Attempt a transparent restart and one retry.
		logging.Log("claude.Client: complete failed, restarting subprocess: %v", err)
		if restartErr := c.start(); restartErr != nil {
			return "", fmt.Errorf("complete: %w (restart also failed: %v)", err, restartErr)
		}
		result, err = c.doComplete(ctx, fullPrompt)
	}
	return result, err
}

// doComplete performs the actual prompt/response round-trip without locking.
// Caller must hold c.mu.
func (c *Client) doComplete(_ context.Context, prompt string) (string, error) {
	if !c.ready {
		return "", fmt.Errorf("client not ready")
	}

	if err := writeUserMessage(c.stdin, prompt); err != nil {
		c.ready = false
		return "", fmt.Errorf("write prompt: %w", err)
	}

	msgs, err := readUntilResult(c.stdout)
	if err != nil {
		c.ready = false
		return "", fmt.Errorf("read response: %w", err)
	}

	// Cache the most recent rate limit event seen in this response.
	for _, msg := range msgs {
		if msg.RateLimitInfo != nil {
			c.lastRateLimit = msg.RateLimitInfo
		}
	}

	// Return the result text from the terminal result message.
	for _, msg := range msgs {
		if msg.Type == "result" && msg.Subtype == "success" {
			return msg.Result, nil
		}
		// Some CLI versions use subtype "error_during_execution" etc.
		// Treat any result message as terminal.
		if msg.Type == "result" {
			if msg.Result != "" {
				return msg.Result, nil
			}
			return "", fmt.Errorf("result subtype=%q", msg.Subtype)
		}
	}

	return "", fmt.Errorf("no result message in response")
}

// RateLimitInfo returns the most recently observed rate-limit info from a
// stream response, or nil if no Complete/Ping call has been made yet.
func (c *Client) RateLimitInfo() *RateLimitInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRateLimit
}

// Close shuts down the subprocess gracefully.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	c.ready = false
	return nil
}

// --------------------------------------------------------------------------
// Package-level singleton
// --------------------------------------------------------------------------

var (
	defaultOnce   sync.Once
	defaultClient *Client
	defaultErr    error
)

// DefaultClient returns (or lazily starts) the package-level singleton Client.
// The singleton uses the default model (claude-haiku) with a clean environment
// that strips all AGENTCTL_* vars to prevent hook recursion.
//
// On first call the subprocess is spawned synchronously. If startup fails, the
// error is returned on every subsequent call (the Once is consumed either way).
func DefaultClient() (*Client, error) {
	defaultOnce.Do(func() {
		defaultClient, defaultErr = NewClient()
	})
	return defaultClient, defaultErr
}
