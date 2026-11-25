package serve

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/artifactpdf"
	"github.com/blsmth/agentctl/internal/artifacts"
)

// PDF export for the viewer (GET /v1/artifacts/{id}/pdf, routes_artifacts.go).
// A render launches a headless browser, so pdfExporter bounds the cost: one
// render at a time process-wide, concurrent requests for the same revision
// share one render, and finished PDFs are kept in memory (never in the
// artifact store, whose only writer is the CLI) keyed by numbered revision.

// RenderPDFFunc writes the PDF of revision rev of artifact id to w; it is
// artifactpdf.Render in production and a stub in tests (Options.RenderPDF).
type RenderPDFFunc func(ctx context.Context, store *artifacts.Store, id string, rev int, w io.Writer) error

const (
	// pdfConcurrency is how many renders may run at once.
	pdfConcurrency = 1
	// pdfQueueWait is how long a render waits for a free slot before the
	// request gets 503.
	pdfQueueWait = 30 * time.Second
	// pdfRetryAfter is the Retry-After (seconds) of that 503.
	pdfRetryAfter = "15"
	// pdfCacheBytes caps the bytes of cached PDFs.
	pdfCacheBytes = 50 << 20
)

// errPDFBusy is a render that waited pdfQueueWait for a slot.
var errPDFBusy = errors.New("another PDF is rendering")

// pdfKey names one numbered revision of one artifact.
type pdfKey struct {
	id  string
	rev int
}

// pdfFlight is one render shared by every request for its key. waiters
// counts the requests still waiting; when the last gives up, the render is
// canceled.
type pdfFlight struct {
	done    chan struct{}
	pdf     []byte
	err     error
	cancel  context.CancelFunc
	waiters int
}

// pdfExporter renders, deduplicates, limits and caches artifact PDFs.
type pdfExporter struct {
	store  *artifacts.Store
	render RenderPDFFunc
	slots  chan struct{}
	wait   time.Duration

	mu      sync.Mutex
	flights map[pdfKey]*pdfFlight
	cache   *pdfCache
	// joined, when set (tests), is called with a flight's waiter count each
	// time a request joins it.
	joined func(waiters int)
}

func newPDFExporter(store *artifacts.Store, render RenderPDFFunc) *pdfExporter {
	if render == nil {
		render = artifactpdf.Render
	}
	return &pdfExporter{
		store:   store,
		render:  render,
		slots:   make(chan struct{}, pdfConcurrency),
		wait:    pdfQueueWait,
		flights: map[pdfKey]*pdfFlight{},
		cache:   newPDFCache(pdfCacheBytes),
	}
}

// get returns the PDF of k: from the cache, from a render already running
// for k, or from a new one. It returns ctx's error when ctx ends first; the
// render goes on as long as another request still waits for it.
func (e *pdfExporter) get(ctx context.Context, k pdfKey) ([]byte, error) {
	e.mu.Lock()
	if pdf, ok := e.cache.get(k); ok {
		e.mu.Unlock()
		return pdf, nil
	}
	f, ok := e.flights[k]
	if !ok {
		fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &pdfFlight{done: make(chan struct{}), cancel: cancel}
		e.flights[k] = f
		go e.run(fctx, k, f)
	}
	f.waiters++
	if e.joined != nil {
		e.joined(f.waiters)
	}
	e.mu.Unlock()

	select {
	case <-f.done:
		return f.pdf, f.err
	case <-ctx.Done():
		e.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			// Nobody wants it: stop it, and let the next request start afresh
			// rather than join a canceled render.
			f.cancel()
			if e.flights[k] == f {
				delete(e.flights, k)
			}
		}
		e.mu.Unlock()
		return nil, ctx.Err()
	}
}

// run renders k once a slot is free and publishes the result to f.
func (e *pdfExporter) run(ctx context.Context, k pdfKey, f *pdfFlight) {
	defer f.cancel()
	pdf, err := e.renderInSlot(ctx, k)
	e.mu.Lock()
	f.pdf, f.err = pdf, err
	if err == nil {
		e.cache.put(k, pdf)
	}
	if e.flights[k] == f {
		delete(e.flights, k)
	}
	e.mu.Unlock()
	close(f.done)
}

func (e *pdfExporter) renderInSlot(ctx context.Context, k pdfKey) ([]byte, error) {
	timer := time.NewTimer(e.wait)
	defer timer.Stop()
	select {
	case e.slots <- struct{}{}:
	case <-timer.C:
		return nil, errPDFBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-e.slots }()
	var buf bytes.Buffer
	if err := e.render(ctx, e.store, k.id, k.rev, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// forget drops every cached revision of id (on delete).
func (e *pdfExporter) forget(id string) {
	e.mu.Lock()
	e.cache.forget(id)
	e.mu.Unlock()
}

// pdfCache is an LRU of rendered PDFs bounded by total bytes. Callers hold
// pdfExporter.mu.
type pdfCache struct {
	max   int
	size  int
	order *list.List // front = most recently used; values are *pdfEntry
	items map[pdfKey]*list.Element
}

type pdfEntry struct {
	key pdfKey
	pdf []byte
}

func newPDFCache(maxBytes int) *pdfCache {
	return &pdfCache{max: maxBytes, order: list.New(), items: map[pdfKey]*list.Element{}}
}

func (c *pdfCache) get(k pdfKey) ([]byte, bool) {
	el, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*pdfEntry).pdf, true
}

// put stores pdf under k, evicting the least recently used entries to stay
// within max. A PDF larger than max is not cached.
func (c *pdfCache) put(k pdfKey, pdf []byte) {
	if len(pdf) > c.max {
		return
	}
	if el, ok := c.items[k]; ok {
		c.remove(el)
	}
	c.items[k] = c.order.PushFront(&pdfEntry{key: k, pdf: pdf})
	c.size += len(pdf)
	for c.size > c.max {
		c.remove(c.order.Back())
	}
}

func (c *pdfCache) forget(id string) {
	for k, el := range c.items {
		if k.id == id {
			c.remove(el)
		}
	}
}

func (c *pdfCache) remove(el *list.Element) {
	e := el.Value.(*pdfEntry)
	c.order.Remove(el)
	delete(c.items, e.key)
	c.size -= len(e.pdf)
}
