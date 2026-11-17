package launchd

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// parsePlist is a minimal XML property-list parser for tests: dict ->
// map[string]any, array -> []any, string/data/integer -> string (data is
// base64-decoded to raw bytes as string), true/false -> bool.
func parsePlist(t *testing.T, data []byte) any {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no <plist> element: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "plist" {
			v, err := plistValue(dec, nil)
			if err != nil {
				t.Fatalf("parsing plist: %v", err)
			}
			return v
		}
	}
}

func plistValue(dec *xml.Decoder, start *xml.StartElement) (any, error) {
	if start == nil {
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if se, ok := tok.(xml.StartElement); ok {
				start = &se
				break
			}
		}
	}
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch el := tok.(type) {
			case xml.EndElement:
				return m, nil
			case xml.StartElement:
				if el.Name.Local != "key" {
					return nil, fmt.Errorf("expected <key>, got <%s>", el.Name.Local)
				}
				var key string
				if err := dec.DecodeElement(&key, &el); err != nil {
					return nil, err
				}
				v, err := plistValue(dec, nil)
				if err != nil {
					return nil, err
				}
				m[key] = v
			}
		}
	case "array":
		var a []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch el := tok.(type) {
			case xml.EndElement:
				return a, nil
			case xml.StartElement:
				v, err := plistValue(dec, &el)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return start.Name.Local == "true", nil
	case "string", "integer":
		var s string
		err := dec.DecodeElement(&s, start)
		return s, err
	case "data":
		var s string
		if err := dec.DecodeElement(&s, start); err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		return string(raw), err
	default:
		return nil, fmt.Errorf("unsupported plist element <%s>", start.Name.Local)
	}
}

// plutilLint runs `plutil -lint` on macOS as an extra check.
func plutilLint(t *testing.T, data []byte) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v: %s", err, out)
	}
}
