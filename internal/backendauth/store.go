package backendauth

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// securityBin is the macOS keychain CLI.
const securityBin = "/usr/bin/security"

const keychainService = "agentctl"

func newPlatformStore() Store {
	if runtime.GOOS == "darwin" {
		return &keychainStore{}
	}
	return &fileStore{dir: defaultCredentialsDir()}
}

// --- macOS keychain ---------------------------------------------------------

// keychainStore keeps keys in the login keychain via /usr/bin/security.
type keychainStore struct{}

func (*keychainStore) Source() string { return SourceKeychain }

func keychainAccount(name string) string { return "backend:" + name }

func (*keychainStore) run(stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := execCommand(ctx, securityBin, args...)
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	c.Stdout = &stdout
	// stderr is dropped: nothing from the tool is ever echoed into an error.
	err := c.Run()
	return stdout.Bytes(), err
}

func (k *keychainStore) Get(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	out, err := k.run("", "find-generic-password", "-s", keychainService, "-a", keychainAccount(name), "-w")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain read failed: %w", err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", ErrNotFound
	}
	return key, nil
}

// Set writes the key by feeding `security -i` on stdin so it never appears in
// argv (visible to every local user through ps). The value is hex-encoded
// (add-generic-password -X) so no quoting rules for the interactive parser
// can be tripped by the key's content.
func (k *keychainStore) Set(name, key string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -X %s\n",
		keychainService, keychainAccount(name), hex.EncodeToString([]byte(key)))
	if _, err := k.run(cmd, "-i"); err != nil {
		return fmt.Errorf("keychain write failed: %w", err)
	}
	return nil
}

func (k *keychainStore) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	_, err := k.run("", "delete-generic-password", "-s", keychainService, "-a", keychainAccount(name))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return ErrNotFound
		}
		return fmt.Errorf("keychain delete failed: %w", err)
	}
	return nil
}

// --- file store -------------------------------------------------------------

// fileStore keeps one 0600 file per backend under a 0700 directory.
type fileStore struct{ dir string }

func (*fileStore) Source() string { return SourceFile }

// defaultCredentialsDir is $XDG_STATE_HOME/agentctl/credentials, falling back
// to ~/.local/state.
func defaultCredentialsDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "agentctl", "credentials")
}

func (f *fileStore) path(name string) (string, error) {
	if f.dir == "" {
		return "", errors.New("cannot resolve credentials directory")
	}
	if err := checkName(name); err != nil {
		return "", err
	}
	return filepath.Join(f.dir, name), nil
}

func (f *fileStore) Get(name string) (string, error) {
	p, err := f.path(name)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("stat credential file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("credential file %s is not a regular file", p)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential file %s has mode %04o; refusing to read it (chmod 600 it or run `agentctl backend login` again)", p, fi.Mode().Perm())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("reading credential file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", ErrNotFound
	}
	return key, nil
}

func (f *fileStore) Set(name, key string) error {
	p, err := f.path(name)
	if err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return fmt.Errorf("creating credentials directory: %w", err)
	}
	if err := os.Chmod(f.dir, 0o700); err != nil {
		return fmt.Errorf("securing credentials directory: %w", err)
	}
	tmp, err := os.CreateTemp(f.dir, ".tmp-*") // 0600
	if err != nil {
		return fmt.Errorf("creating credential file: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.WriteString(key + "\n")
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		return errors.New("writing credential file failed")
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("installing credential file: %w", err)
	}
	return nil
}

func (f *fileStore) Delete(name string) error {
	p, err := f.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("removing credential file: %w", err)
	}
	return nil
}
