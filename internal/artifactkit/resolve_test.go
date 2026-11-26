package artifactkit

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	g := &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})}, Delay: []int{1}}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func imageSpec(blocks string) []byte {
	return []byte(`{"kind":"diagram","title":"T","blocks":[` + blocks + `]}`)
}

func srcs(t *testing.T, data []byte) []string {
	t.Helper()
	var spec struct {
		Blocks []struct {
			Src    string `json:"src"`
			Images []struct {
				Src string `json:"src"`
			} `json:"images"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range spec.Blocks {
		if b.Src != "" {
			out = append(out, b.Src)
		}
		for _, im := range b.Images {
			out = append(out, im.Src)
		}
	}
	return out
}

func TestResolveImagesInlines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shots/a.png", pngBytes(t))
	writeFile(t, dir, "b.gif", gifBytes(t))
	spec := imageSpec(`{"type":"image","src":"shots/a.png","alt":"A"},{"type":"image","images":[{"src":"b.gif","alt":"B"},{"src":"https://x.test/c.png","alt":"C"}]}`)
	out, err := ResolveImages(spec, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := srcs(t, out)
	if !strings.HasPrefix(got[0], "data:image/png;base64,") || !strings.HasPrefix(got[1], "data:image/gif;base64,") || got[2] != "https://x.test/c.png" {
		t.Errorf("srcs = %.40v", got)
	}
	if errs := Validate(out); errs != nil {
		t.Errorf("resolved spec must validate: %v", errs)
	}
}

func TestResolveImagesCwdAndHome(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.png", pngBytes(t))
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	for _, src := range []string{"a.png", "~/a.png", filepath.Join(dir, "a.png")} {
		out, err := ResolveImages(imageSpec(`{"type":"image","src":"`+src+`","alt":"A"}`), ".", 0)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if !strings.HasPrefix(srcs(t, out)[0], "data:image/png;base64,") {
			t.Errorf("%s not inlined", src)
		}
	}
}

func TestResolveImagesHTTPSUntouched(t *testing.T) {
	spec := imageSpec(`{"type":"image","src":"https://example.com/a.png","alt":"A"}`)
	out, err := ResolveImages(spec, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, spec) {
		t.Errorf("spec changed: %s", out)
	}
}

func TestResolveImagesErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.png", pngBytes(t))
	writeFile(t, dir, "x.svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`))
	writeFile(t, dir, "fake.png", []byte("just text"))
	writeFile(t, dir, "big.png", append(pngBytes(t), make([]byte, MaxImageBytes)...))
	tests := []struct {
		name, blocks string
		max          int64
		want         string
	}{
		{"missing", `{"type":"image","src":"nope.png","alt":"A"}`, 0, "blocks[0].src: reading image"},
		{"svg", `{"type":"image","src":"x.svg","alt":"A"}`, 0, "blocks[0].src: image/svg+xml is not supported (png, jpeg, gif, webp)"},
		{"not an image", `{"type":"image","src":"fake.png","alt":"A"}`, 0, "text/plain is not supported"},
		{"per image", `{"type":"image","src":"big.png","alt":"A"}`, 0, "big.png is 8.0 MB, over the 8.0 MB per-image limit"},
		{"total", `{"type":"image","src":"ok.png","alt":"A"},{"type":"image","images":[{"src":"ok.png","alt":"B"}]}`, int64(len(pngBytes(t))) + 1, "blocks[1].images[0].src:"},
		{"dir", `{"type":"image","src":".","alt":"A"}`, 0, "is a directory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveImages(imageSpec(tc.blocks), dir, tc.max)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateImageShapes(t *testing.T) {
	tests := []struct{ name, block, path string }{
		{"no alt", `{"type":"image","src":"a.png"}`, "blocks[0].alt"},
		{"both", `{"type":"image","src":"a.png","alt":"x","images":[{"src":"b.png","alt":"y"}]}`, "blocks[0].images"},
		{"neither", `{"type":"image"}`, "blocks[0].src"},
		{"empty images", `{"type":"image","images":[]}`, "blocks[0].images"},
		{"bad width", `{"type":"image","src":"a.png","alt":"x","width":"tiny"}`, "blocks[0].width"},
		{"http", `{"type":"image","src":"http://a/b.png","alt":"x"}`, "blocks[0].src"},
		{"ftp", `{"type":"image","src":"ftp://a/b.png","alt":"x"}`, "blocks[0].src"},
		{"gallery alt", `{"type":"image","images":[{"src":"a.png","alt":"x"}],"alt":"y"}`, "blocks[0].alt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := Validate(imageSpec(tc.block))
			found := false
			for _, e := range errs {
				found = found || e.Path == tc.path
			}
			if !found {
				t.Errorf("want error at %s, got %v", tc.path, errs)
			}
		})
	}
}

func TestRenderEmbedsDataURI(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.png", pngBytes(t))
	spec, err := ResolveImages(imageSpec(`{"type":"image","src":"a.png","alt":"A"}`), dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(page.HTML, []byte("data:image/png;base64,")) {
		t.Error("page lacks the data URI")
	}
}
