package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/uploads"
)

const (
	// maxImages bounds the images in one upload request and in one message.
	maxImages = 4
	// maxImageBytes bounds one image. iOS hands over a photo-library JPEG of
	// a few MB; a full-resolution screenshot PNG is well under this.
	maxImageBytes = 10 << 20
	// maxUploadBody bounds a whole upload request (all images plus framing).
	maxUploadBody = maxImages*maxImageBytes + 1<<20
	// imageField is the multipart field the image parts use.
	imageField = "image"
)

type uploadOut struct {
	Images []string `json:"images"`
}

// handleUpload stores the images of a multipart request under the window's
// runtime dir and returns their IDs; a later reply or queue names them in
// "images". It shares the write guards (device auth, CSRF, allow_write, rate
// limit, audit) but takes multipart rather than JSON.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	d, _ := DeviceFrom(r.Context())
	a := s.auditFor(r, d, "upload")
	out := s.upload(w, r, &a)
	if out.status >= 400 {
		a.Result = out.code
		writeError(w, out.status, out.code, out.msg)
	} else {
		a.Result = "ok"
		writeJSON(w, out.status, out.body)
	}
	s.recordAudit(a)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, a *auditEntry) outcome {
	if wait := s.limiter.reserve(a.DeviceID, s.now()); wait > 0 {
		return failure(http.StatusTooManyRequests, codeRateLimited, "too many write actions; try again shortly")
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
		return failure(http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be multipart/form-data")
	}
	rw, out, ok := s.resolveWindow(r)
	if !ok {
		return out
	}
	a.Session, a.Window = rw.group.Session, rw.window.WindowID
	if rw.group.RuntimeDir == "" {
		return failure(http.StatusConflict, codeActionFailed, "this window has no runtime directory")
	}
	images, out, ok := readImages(http.MaxBytesReader(w, r.Body, maxUploadBody), r.Header.Get("Content-Type"))
	if !ok {
		return out
	}
	h := sha256.New()
	exts := make([]string, len(images))
	for i, img := range images {
		ext, err := uploads.ExtFor(img)
		if err != nil {
			return failure(http.StatusBadRequest, codeBadRequest, "only png, jpeg, gif and webp images are accepted")
		}
		exts[i] = ext
		h.Write(img)
	}
	a.BodySHA256 = hex.EncodeToString(h.Sum(nil))
	s.auditAttempt(*a)

	ids := make([]string, 0, len(images))
	for i, img := range images {
		id, err := uploads.Save(rw.group.RuntimeDir, rw.window.WindowID, exts[i], img)
		if err != nil {
			logging.Warn("serve: upload %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
			return failure(http.StatusInternalServerError, codeInternal, "storing the image failed")
		}
		ids = append(ids, id)
	}
	// Prune on write too, so a machine whose janitor is idle does not collect
	// screenshots indefinitely.
	if _, err := uploads.Prune(rw.group.RuntimeDir, time.Now(), uploads.MaxAge); err != nil {
		logging.Warn("serve: pruning uploads: %v", err)
	}
	return success(uploadOut{Images: ids})
}

// readImages reads the "image" parts of a multipart body, enforcing the
// per-image and count limits. Nothing is spilled to disk.
func readImages(body io.Reader, contentType string) ([][]byte, outcome, bool) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, failure(http.StatusBadRequest, codeBadRequest, "missing multipart boundary"), false
	}
	mr := multipart.NewReader(body, params["boundary"])
	var images [][]byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, tooLargeOr(err, "reading upload"), false
		}
		if part.FormName() != imageField {
			continue
		}
		if len(images) == maxImages {
			return nil, failure(http.StatusBadRequest, codeBadRequest, fmt.Sprintf("at most %d images per upload", maxImages)), false
		}
		data, err := io.ReadAll(io.LimitReader(part, maxImageBytes+1))
		if err != nil {
			return nil, tooLargeOr(err, "reading image"), false
		}
		if len(data) > maxImageBytes {
			return nil, failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("an image exceeds %d MB", maxImageBytes>>20)), false
		}
		if len(data) == 0 {
			return nil, failure(http.StatusBadRequest, codeBadRequest, "empty image"), false
		}
		images = append(images, data)
	}
	if len(images) == 0 {
		return nil, failure(http.StatusBadRequest, codeBadRequest, `no "image" part`), false
	}
	return images, outcome{}, true
}

func tooLargeOr(err error, msg string) outcome {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, "upload too large")
	}
	return failure(http.StatusBadRequest, codeBadRequest, msg)
}

// withImages returns the text to type: the message followed by one
// "[Screenshot: <abs path>]" line per image, which the agent opens with its
// file-read tool. Each ID must be one this window's upload produced.
func withImages(rw resolved, in replyIn) (string, outcome, bool) {
	if len(in.Images) == 0 {
		return in.Text, outcome{}, true
	}
	var lines []string
	seen := map[string]bool{}
	for _, id := range in.Images {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := uploads.Path(rw.group.RuntimeDir, rw.window.WindowID, id)
		if err != nil {
			return "", failure(http.StatusBadRequest, codeBadRequest, "unknown image; upload it again"), false
		}
		if out, ok := checkText("image path", p); !ok {
			return "", out, false
		}
		lines = append(lines, "[Screenshot: "+p+"]")
	}
	block := strings.Join(lines, "\n")
	if strings.TrimSpace(in.Text) == "" {
		return block, outcome{}, true
	}
	return strings.TrimRight(in.Text, "\n") + "\n\n" + block, outcome{}, true
}
