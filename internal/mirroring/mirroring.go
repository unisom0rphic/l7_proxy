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
	"time"

	"github.com/unisom0rphic/l7proxy/internal/metrics"
)

type Target struct {
	URL   *url.URL
	Ratio float64
}

type Job struct {
	method   string
	body     []byte
	header   http.Header
	path     string
	rawQuery string
	targets  []Target
}

func NewJob(method string, body []byte, header http.Header, path, rawQuery string, targets []Target) Job {
	return Job{
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

type Pool struct {
	client  *http.Client
	queue   chan Job
	wg      sync.WaitGroup
	metrics *metrics.MirrorMetrics

	mu      sync.RWMutex
	stopped bool
}

// NewModule starts a fixed pool of mirroring workers. Non-positive values fall
// back to defaults.
func NewPool(workers, queueSize int, mirrorMetrics *metrics.MirrorMetrics) *Pool {
	if workers <= 0 {
		workers = defaultWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}

	p := &Pool{
		client:  &http.Client{Timeout: mirrorTimeout},
		queue:   make(chan Job, queueSize),
		metrics: mirrorMetrics,
	}

	for range workers {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *Pool) record(result string) {
	if p.metrics != nil {
		p.metrics.Observe(result)
	}
}

func (p *Pool) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.queue)
	p.mu.Unlock()

	slog.Info("closing mirroring module")
	p.wg.Wait()
}

// TrySubmit enqueues a job without blocking the caller. A full queue, or a
// module that has already stopped, counts as a drop.
func (p *Pool) TrySubmit(j Job) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.stopped {
		p.record(metrics.MirrorDropped)
		return
	}

	select {
	case p.queue <- j:
	default:
		p.record(metrics.MirrorDropped)
	}
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for j := range p.queue {
		for _, t := range j.targets {
			//nolint:gosec // Sampling doesn't require strong generator
			if !sample(t.Ratio, rand.Float64()) {
				continue
			}
			if err := p.send(j, t); err != nil {
				slog.Warn("mirror request failed", "mirror", t.URL.String(), "error", err)
				p.record(metrics.MirrorFailed)
				continue
			}
			p.record(metrics.MirrorSent)
		}
	}
}

func (p *Pool) send(j Job, t Target) error {
	u := *t.URL
	u.Path = j.path
	u.RawQuery = j.rawQuery

	//nolint:noctx // Timeout handled by client.Do() on :150
	req, err := http.NewRequest(j.method, u.String(), bytes.NewReader(j.body))
	if err != nil {
		return err
	}
	req.Header = j.header.Clone()
	removeHopByHopHeaders(req.Header)

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// The body has to be drained for the connection to be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	return nil
}

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
