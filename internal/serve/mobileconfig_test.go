package serve

import (
	"strings"
	"testing"
)

func TestMobileconfig(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir, "Host & <Co>", newClock().Now())
	if err != nil {
		t.Fatal(err)
	}
	data := Mobileconfig(ca)
	plutilLint(t, data)

	root, ok := parsePlist(t, data).(map[string]any)
	if !ok {
		t.Fatal("top level is not a dict")
	}
	if root["PayloadType"] != "Configuration" || root["PayloadVersion"] != "1" {
		t.Errorf("profile header wrong: %v", root)
	}
	for _, k := range []string{"PayloadUUID", "PayloadIdentifier", "PayloadDisplayName"} {
		if s, _ := root[k].(string); s == "" {
			t.Errorf("profile missing %s", k)
		}
	}
	content, ok := root["PayloadContent"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("PayloadContent = %#v", root["PayloadContent"])
	}
	payload := content[0].(map[string]any)
	if payload["PayloadType"] != "com.apple.security.root" {
		t.Errorf("payload type = %v", payload["PayloadType"])
	}
	if payload["PayloadContent"] != string(ca.Cert.Raw) {
		t.Error("payload does not contain the CA certificate DER")
	}
	if payload["PayloadDisplayName"] != ca.Cert.Subject.CommonName {
		t.Errorf("display name = %v (escaping broken?)", payload["PayloadDisplayName"])
	}
	if payload["PayloadUUID"] == root["PayloadUUID"] {
		t.Error("payload and profile share a UUID")
	}
	uuid, _ := payload["PayloadUUID"].(string)
	if len(uuid) != 36 || strings.Count(uuid, "-") != 4 {
		t.Errorf("UUID %q is malformed", uuid)
	}

	// Stable per CA: re-downloading replaces the profile instead of adding one.
	if string(Mobileconfig(ca)) != string(data) {
		t.Error("profile is not deterministic for the same CA")
	}
}
