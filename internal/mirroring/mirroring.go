package mirroring

import (
	"bytes"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MirrorTarget is one destination a request may be copied to. Ratio is the
// fraction of requests to send, in [0, 1].
type MirrorTarget struct {
	URL   *url.URL
	Ratio float64
}

// MirrorJob is a single request to be copied to every target that samples in.
type MirrorJob struct {
	method   string
	body     []byte
	header   http.Header
	path     string
	rawQuery string
	targets  []MirrorTarget
}

// NewJob captures an outbound request. path and rawQuery are the values the
// primary upstream received, so a mirror sees the same URI it would have.
func NewJob(method string, body []byte, header http.Header, path, rawQuery string, targets []MirrorTarget) MirrorJob {
	return MirrorJob{
		method:   method,
		body:     body,
		header:   header,
		path:     path,
		rawQuery: rawQuery,
		targets:  targets,
	}
}

const (
	defaultWorkers   = 10
	defaultQueueSize = 100
	mirrorTimeout    = 5 * time.Second
)

type MirrorModule struct {
	client *http.Client
	queue  chan MirrorJob
	wg     sync.WaitGroup

	mu      sync.RWMutex
	stopped bool

	sent, failed, dropped atomic.Uint64
}

// NewModule starts a fixed pool of mirroring workers. Non-positive values fall
// back to defaults: a pool of zero workers would silently drop every job.
func NewModule(workers, queueSize int) *MirrorModule {
	if workers <= 0 {
		workers = defaultWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}

	m := &MirrorModule{
		client: &http.Client{Timeout: mirrorTimeout},
		queue:  make(chan MirrorJob, queueSize),
	}

	for range workers {
		m.wg.Add(1)
		go m.worker()
	}
	return m
}

// Stop drains the queue, waits for in-flight mirrors, and is safe to call more
// than once.
func (m *MirrorModule) Stop() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	close(m.queue)
	m.mu.Unlock()

	slog.Info("closing mirroring module")
	m.wg.Wait()
}

// TrySubmit enqueues a job without blocking the caller. A full queue, or a
// module that has already stopped, counts as a drop.
func (m *MirrorModule) TrySubmit(j MirrorJob) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.stopped {
		m.dropped.Add(1)
		return
	}

	select {
	case m.queue <- j:
	default:
		m.dropped.Add(1)
	}
}

func (m *MirrorModule) worker() {
	defer m.wg.Done()
	for j := range m.queue {
		for _, t := range j.targets {
			if !sample(t.Ratio, rand.Float64()) {
				continue
			}
			if err := m.send(j, t); err != nil {
				slog.Warn("mirror request failed", "mirror", t.URL.String(), "error", err)
				m.failed.Add(1)
				continue
			}
			m.sent.Add(1)
		}
	}
}

func (m *MirrorModule) send(j MirrorJob, t MirrorTarget) error {
	u := *t.URL
	u.Path = j.path
	u.RawQuery = j.rawQuery

	req, err := http.NewRequest(j.method, u.String(), bytes.NewReader(j.body))
	if err != nil {
		return err
	}
	req.Header = j.header.Clone()
	removeHopByHopHeaders(req.Header)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// The body has to be drained for the connection to be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	return nil
}

// sample reports whether a request is mirrored, given a random value in [0, 1).
func sample(ratio, rnd float64) bool {
	switch {
	case ratio <= 0:
		return false
	case ratio >= 1:
		return true
	default:
		return rnd < ratio
	}
}

// hopByHopHeaders apply to a single hop and must not be forwarded
// (RFC 9110, section 7.6.1).
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// removeHopByHopHeaders drops hop-by-hop headers, including any header the
// Connection field names as hop-by-hop for this message.
func removeHopByHopHeaders(h http.Header) {
	for _, value := range h["Connection"] {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}
