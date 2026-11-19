package backendauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

const nousBody402 = `{"status":402,"message":"Insufficient available credits for this inference request. Add credits or lower the requested maximum output tokens."}`

func TestIsNoCredits(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"402 exact body", 402, nousBody402, true},
		{"402 non-JSON", 402, "payment required", true},
		{"402 empty", 402, "", true},
		{"403 code", 403, `{"error":{"code":"insufficient_credits_for_paid_model"}}`, true},
		{"404 code", 404, `{"error":{"code":"insufficient_credits_for_paid_model"}}`, true},
		{"403 WAF non-JSON", 403, "<html>blocked</html>", false},
		{"403 other code", 403, `{"error":{"code":"nope"}}`, false},
		{"401", 401, `{"error":{"code":"insufficient_credits_for_paid_model"}}`, false},
		{"500", 500, "", false},
		{"200", 200, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNoCredits(tt.status, []byte(tt.body)); got != tt.want {
				t.Errorf("IsNoCredits = %v, want %v", got, tt.want)
			}
		})
	}
}

func creditEnv(t *testing.T) (notified *atomic.Int32) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	notified = &atomic.Int32{}
	old := creditNotify
	creditNotify = func(string) { notified.Add(1) }
	t.Cleanup(func() { creditNotify = old })
	return notified
}

func TestCreditStateLifecycle(t *testing.T) {
	notified := creditEnv(t)
	now := time.Now()
	if _, fresh := NoCreditsFresh("nous", now); fresh {
		t.Fatal("fresh with no state dir content")
	}
	if !markNoCredits("nous", now) {
		t.Fatal("first mark should transition")
	}
	if markNoCredits("nous", now.Add(time.Minute)) {
		t.Fatal("second mark must not transition")
	}
	if notified.Load() != 1 {
		t.Fatalf("notified %d times, want 1", notified.Load())
	}
	st, fresh := NoCreditsFresh("nous", now.Add(2*time.Minute))
	if !fresh || st.NoCreditsSince.IsZero() {
		t.Fatalf("state = %+v fresh=%v", st, fresh)
	}
	if _, fresh := NoCreditsFresh("nous", now.Add(CreditsFreshFor+2*time.Minute)); fresh {
		t.Error("marker should be stale after CreditsFreshFor")
	}
	// A stale re-confirmation renews without a new notification.
	if markNoCredits("nous", now.Add(CreditsFreshFor+3*time.Minute)) {
		t.Error("re-confirmation transitioned")
	}
	d, _ := config.StateDir()
	fi, err := os.Stat(filepath.Join(d, "backends", "nous.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v mode=%v", err, fi)
	}
	ClearNoCredits("nous")
	if _, fresh := NoCreditsFresh("nous", time.Now()); fresh {
		t.Error("still fresh after clear")
	}
	if !markNoCredits("nous", time.Now()) || notified.Load() != 2 {
		t.Error("mark after clear should transition and notify again")
	}
}

func TestCreditStateCorruptIsClear(t *testing.T) {
	creditEnv(t)
	d, _ := config.StateDir()
	_ = os.MkdirAll(filepath.Join(d, "backends"), 0o700)
	_ = os.WriteFile(filepath.Join(d, "backends", "nous.json"), []byte("{not json"), 0o600)
	if _, fresh := NoCreditsFresh("nous", time.Now()); fresh {
		t.Fatal("corrupt marker must read as clear")
	}
	if !markNoCredits("nous", time.Now()) {
		t.Fatal("corrupt marker should be replaceable (transition)")
	}
}

func TestCreditStateBadName(t *testing.T) {
	creditEnv(t)
	for _, n := range []string{"", "../x", "a/b", ".hidden"} {
		if markNoCredits(n, time.Now()) {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestMarkNoCreditsConcurrentNotifiesOnce(t *testing.T) {
	notified := creditEnv(t)
	var wg sync.WaitGroup
	var transitions atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if markNoCredits("nous", time.Now()) {
				transitions.Add(1)
			}
		}()
	}
	wg.Wait()
	if transitions.Load() != 1 || notified.Load() != 1 {
		t.Fatalf("transitions=%d notified=%d, want 1/1", transitions.Load(), notified.Load())
	}
}

func TestNoCreditsMessage(t *testing.T) {
	now := time.Now()
	m := NoCreditsMessage("nous", CreditState{NoCreditsSince: now.Add(-3 * time.Hour)}, now)
	for _, s := range []string{"Nous Portal account nous is out of credits (since 3h ago)", "https://portal.nousresearch.com", ":free model", "agentctl backend status --verify"} {
		if !strings.Contains(m, s) {
			t.Errorf("message %q missing %q", m, s)
		}
	}
}

func TestVerify402SetsAndSuccessClears(t *testing.T) {
	creditEnv(t)
	status := 402
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(nousBody402))
	}))
	defer srv.Close()
	b := config.BackendConfig{BaseURL: srv.URL, Model: "a/b"}
	err := Verify(context.Background(), "nous", b, "k")
	if !errors.Is(err, ErrNoCredits) {
		t.Fatalf("err = %v, want ErrNoCredits", err)
	}
	if _, fresh := NoCreditsFresh("nous", time.Now()); !fresh {
		t.Error("marker not set by 402")
	}
	status = 200
	if err := Verify(context.Background(), "nous", b, "k"); err != nil {
		t.Fatal(err)
	}
	if _, fresh := NoCreditsFresh("nous", time.Now()); fresh {
		t.Error("marker not cleared by success")
	}
}
