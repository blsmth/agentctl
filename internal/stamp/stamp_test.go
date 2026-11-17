package stamp

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTryConcurrentOnlyOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.stamp")
	now := time.Now()
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if Try(path, time.Minute, now) {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := won.Load(); got != 1 {
		t.Fatalf("winners = %d, want 1", got)
	}
}

func TestTryInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.stamp")
	base := time.Now()
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"first call", base, true},
		{"within interval", base.Add(30 * time.Second), false},
		{"after interval", base.Add(61 * time.Second), true},
		{"throttled again after re-stamp", base.Add(62 * time.Second), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Try(path, time.Minute, tc.at); got != tc.want {
				t.Fatalf("Try = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTryUnwritablePathDenies(t *testing.T) {
	if Try(filepath.Join(t.TempDir(), "missing", "s.stamp"), time.Minute, time.Now()) {
		t.Fatal("Try succeeded on an unopenable stamp")
	}
}
