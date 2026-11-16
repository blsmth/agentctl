package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
)

// nousBackend resolves name from the global config and requires kind nous.
func nousBackend(name string) (config.BackendConfig, error) {
	b, err := config.Load().ResolveBackend(name)
	if err != nil {
		return b, err
	}
	if !b.IsNous() {
		return b, fmt.Errorf("backend %q is not a nous backend (kind %q); only nous backends have stored credentials", name, kindLabel(b))
	}
	return b, nil
}

func kindLabel(b config.BackendConfig) string {
	if b.Kind == "" {
		return config.BackendKindOllama
	}
	return b.Kind
}

// readAPIKey reads the key from stdin (one line) or, on a terminal, from a
// no-echo prompt. The prompt goes to stderr so stdout stays scriptable.
func readAPIKey(in *os.File, fromStdin bool, prompt string, errOut io.Writer) (string, error) {
	var raw string
	switch {
	case fromStdin:
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return "", fmt.Errorf("reading key from stdin: %w", err)
		}
		raw = line
	case term.IsTerminal(int(in.Fd())):
		_, _ = fmt.Fprint(errOut, prompt)
		b, err := term.ReadPassword(int(in.Fd()))
		_, _ = fmt.Fprintln(errOut)
		if err != nil {
			return "", fmt.Errorf("reading key: %w", err)
		}
		raw = string(b)
	default:
		return "", errors.New("stdin is not a terminal; pass --stdin to read the key from a pipe")
	}
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", errors.New("no key entered")
	}
	return key, nil
}

// RunBackendLogin stores an API key for a nous backend and, unless noVerify,
// checks it with one paid one-token request.
func RunBackendLogin(name string, fromStdin, noVerify bool) error {
	b, err := nousBackend(name)
	if err != nil {
		return err
	}
	if b.APIKeyCmd != "" {
		fmt.Fprintf(os.Stderr, "note: backend %q has api_key_cmd set; it takes precedence over the stored key\n", name)
	}
	key, err := readAPIKey(os.Stdin, fromStdin, fmt.Sprintf("API key for backend %q: ", name), os.Stderr)
	if err != nil {
		return err
	}
	if err := backendauth.DefaultStore.Set(name, key); err != nil {
		return fmt.Errorf("storing key: %w", err)
	}
	fmt.Printf("Stored key for backend %q (%s).\n", name, backendauth.DefaultStore.Source())
	if noVerify {
		return nil
	}
	fmt.Println("Verifying with a one-token request (this bills a fraction of a cent)...")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := backendauth.Verify(ctx, b, key); err != nil {
		return fmt.Errorf("key stored, but verification failed: %w", err)
	}
	fmt.Println("Key verified.")
	return nil
}

// RunBackendLogout deletes the stored key for a backend.
func RunBackendLogout(name string) error {
	if err := backendauth.DefaultStore.Delete(name); err != nil {
		if errors.Is(err, backendauth.ErrNotFound) {
			fmt.Printf("No stored key for backend %q.\n", name)
			return nil
		}
		return err
	}
	fmt.Printf("Removed stored key for backend %q.\n", name)
	return nil
}

// RunBackendStatus lists every configured backend with its kind, auth mode and
// whether a credential is available (and from where). It never prints a key.
// With verify it makes one paid one-token request per nous backend.
func RunBackendStatus(verify bool) error {
	cfg := config.Load()
	if len(cfg.Backends) == 0 {
		fmt.Println("No backends configured.")
		return nil
	}
	names := make([]string, 0, len(cfg.Backends))
	for n := range cfg.Backends {
		names = append(names, n)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "NAME\tKIND\tAUTH\tCREDENTIAL"
	if verify {
		header += "\tVERIFY"
	}
	_, _ = fmt.Fprintln(tw, header)
	for _, n := range names {
		b := cfg.Backends[n]
		if !b.IsNous() {
			row := fmt.Sprintf("%s\t%s\t-\t-", n, kindLabel(b))
			if verify {
				row += "\t-"
			}
			_, _ = fmt.Fprintln(tw, row)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		key, source, err := backendauth.Lookup(ctx, n, b)
		cred := "no"
		if err == nil {
			cred = "yes (" + source + ")"
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s", n, b.Kind, b.Auth, cred)
		if verify {
			res := "skipped (no credential)"
			if err == nil {
				if verr := backendauth.Verify(ctx, b, key); verr != nil {
					res = "FAILED: " + verr.Error()
				} else {
					res = "ok"
				}
			}
			row += "\t" + res
		}
		cancel()
		_, _ = fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}
