package serve

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

func orderEnv(t *testing.T) (*writeEnv, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "group-order.json")
	e := newWriteEnvWith(t, true, func(o *Options) {
		o.GroupOrderPath = func() (string, error) { return path, nil }
	})
	e.fleet.set(
		fleetwatch.Group{Session: "s-a", Key: "/a"},
		fleetwatch.Group{Session: "s-b", Key: "/b"},
		fleetwatch.Group{Session: "s-c", Key: "/c"},
	)
	return e, path
}

func TestGroupOrderWritesMergedOrder(t *testing.T) {
	e, path := orderEnv(t)
	if err := fleet.WriteGroupOrder(path, []string{"/a", "/away", "/b", "/c"}); err != nil {
		t.Fatal(err)
	}
	w := e.post("/v1/group-order", `{"keys":["/c","/a","/b","/c","/unknown"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	// "/away" keeps the slot right after the key that preceded it ("/a").
	want := []string{"/c", "/a", "/away", "/b"}
	if got := fleet.ReadGroupOrder(path); !slices.Equal(got, want) {
		t.Errorf("saved = %v, want %v", got, want)
	}
}

func TestGroupOrderRejectsBadBodies(t *testing.T) {
	e, path := orderEnv(t)
	tooMany := `["` + strings.Repeat(`/a","`, maxGroupOrderKeys) + `/a"]`
	for name, body := range map[string]string{
		"non-string":  `{"keys":[1]}`,
		"not a list":  `{"keys":"/a"}`,
		"empty key":   `{"keys":[""]}`,
		"oversized":   `{"keys":["` + strings.Repeat("x", maxGroupKeyBytes+1) + `"]}`,
		"control":     `{"keys":["/a\u0000"]}`,
		"too many":    `{"keys":` + tooMany + `}`,
		"extra field": `{"keys":[],"x":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if w := e.post("/v1/group-order", body); w.Code != http.StatusBadRequest {
				t.Errorf("status %d: %s", w.Code, w.Body)
			}
			if got := fleet.ReadGroupOrder(path); got != nil {
				t.Errorf("wrote %v", got)
			}
		})
	}
}
