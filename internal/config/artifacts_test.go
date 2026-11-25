package config

import (
	"reflect"
	"testing"
)

// A project-local config cannot choose the PDF browser binary, and Save never
// writes the block into the project layer.
func TestProjectLayerArtifactsIgnored(t *testing.T) {
	cfg := loadWith(t, `{"artifacts":{"pdf_browser":"/usr/bin/good"}}`, `{"artifacts":{"pdf_browser":"/tmp/evil"}}`)
	if want := (ArtifactsConfig{PDFBrowser: "/usr/bin/good"}); !reflect.DeepEqual(cfg.Artifacts, want) {
		t.Fatalf("Artifacts = %+v, want global %+v", cfg.Artifacts, want)
	}
	if !warnedContaining(`"artifacts"`) {
		t.Error("ignored artifacts key was not reported")
	}
	cfg.Artifacts.PDFBrowser = "/tmp/other"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	layer, err := readLayer(FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if layer.Artifacts.PDFBrowser == "/tmp/other" {
		t.Fatalf("project layer saved artifacts: %+v", layer.Artifacts)
	}
}
