package mirroring

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
)

func newTestModule(t *testing.T, workers, queueSize int) (*MirrorModule, *metrics.MirrorMetrics) {
	t.Helper()

	mc := metrics.NewMirrorMetrics()
	m := NewModule(workers, queueSize, mc)
	t.Cleanup(m.Stop)
	return m, mc
}

func counterValue(t *testing.T, mc *metrics.MirrorMetrics, result string) float64 {
	t.Helper()
	return testutil.ToFloat64(mc.RequestsTotal.WithLabelValues(result))
}

func TestSample(t *testing.T) {
	tests := []struct {
		name  string
		ratio float64
		rnd   float64
		want  bool
	}{
		{"zero never mirrors", 0, 0, false},
		{"zero never mirrors at high rnd", 0, 0.999, false},
		{"one always mirrors", 1, 0.999, true},
		{"one mirrors at zero rnd", 1, 0, true},
		{"below ratio mirrors", 0.5, 0.499, true},
		{"at ratio does not mirror", 0.5, 0.5, false},
		{"above ratio does not mirror", 0.5, 0.501, false},
		{"out of range mirrors", 1.5, 0.999, true},
		{"negative never mirrors", -0.5, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sample(tc.ratio, tc.rnd); got != tc.want {
				t.Errorf("sample(%v, %v) = %v, want %v", tc.ratio, tc.rnd, got, tc.want)
			}
		})
	}
}

type mirrorRequest struct {
	method string
	path   string
	query  string
	body   string
	host   string
	header http.Header
}

// newMirrorServer returns a target URL that reports every request it receives
// on got.
func newMirrorServer(t *testing.T, got chan<- mirrorRequest) *url.URL {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- mirrorRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(body),
			host:   r.Host,
			header: r.Header.Clone(),
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse mirror url: %v", err)
	}
	return u
}

func TestMirrorSendsRequestShape(t *testing.T) {
	got := make(chan mirrorRequest, 1)
	target := newMirrorServer(t, got)

	m, mc := newTestModule(t, 1, 1)

	m.TrySubmit(NewJob(
		http.MethodPost,
		[]byte("payload"),
		http.Header{
			"Content-Type": {"text/plain"},
			"X-Trace":      {"abc"},
			"Connection":   {"keep-alive, X-Hop"},
			"Keep-Alive":   {"timeout=5"},
			"X-Hop":        {"dropped-by-connection"},
		},
		"/api/orders/42",
		"a=1&b=2",
		[]MirrorTarget{{URL: target, Ratio: 1}},
	))

	select {
	case r := <-got:
		if r.method != http.MethodPost {
			t.Errorf("method: want POST, got %q", r.method)
		}
		if r.path != "/api/orders/42" {
			t.Errorf("path: want /api/orders/42, got %q", r.path)
		}
		if r.query != "a=1&b=2" {
			t.Errorf("query: want a=1&b=2, got %q", r.query)
		}
		if r.body != "payload" {
			t.Errorf("body: want payload, got %q", r.body)
		}
		if r.host != target.Host {
			t.Errorf("host: want the mirror's own host %q, got %q", target.Host, r.host)
		}
		if r.header.Get("X-Trace") != "abc" {
			t.Errorf("X-Trace: want abc, got %q", r.header.Get("X-Trace"))
		}
		if r.header.Get("Content-Type") != "text/plain" {
			t.Errorf("Content-Type: want text/plain, got %q", r.header.Get("Content-Type"))
		}
		for _, h := range []string{"Connection", "Keep-Alive", "X-Hop"} {
			if v := r.header.Get(h); v != "" {
				t.Errorf("hop-by-hop %s: want it stripped, got %q", h, v)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mirror never received the request")
	}

	// Stop drains the queue and waits for in-flight mirrors, so the counters
	// have settled by the time it returns.
	m.Stop()
	if n := counterValue(t, mc, metrics.MirrorSent); n != 1 {
		t.Errorf("sent: want 1, got %v", n)
	}
}

func TestMirrorSkipsRatioZero(t *testing.T) {
	got := make(chan mirrorRequest, 1)
	target := newMirrorServer(t, got)

	// Both jobs have to fit: TrySubmit drops rather than blocks on a full queue.
	m, _ := newTestModule(t, 1, 2)

	m.TrySubmit(NewJob(http.MethodGet, nil, http.Header{}, "/skipped", "",
		[]MirrorTarget{{URL: target, Ratio: 0}}))

	// A job that does get mirrored proves the worker moved past the first one.
	m.TrySubmit(NewJob(http.MethodGet, nil, http.Header{}, "/sent", "",
		[]MirrorTarget{{URL: target, Ratio: 1}}))

	select {
	case r := <-got:
		if r.path != "/sent" {
			t.Fatalf("mirror received %q, want only /sent", r.path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mirror never received the second job")
	}
}

func TestMirrorFansOutToEveryTarget(t *testing.T) {
	first := make(chan mirrorRequest, 1)
	second := make(chan mirrorRequest, 1)
	targets := []MirrorTarget{
		{URL: newMirrorServer(t, first), Ratio: 1},
		{URL: newMirrorServer(t, second), Ratio: 1},
	}

	m, _ := newTestModule(t, 1, 1)

	m.TrySubmit(NewJob(http.MethodGet, nil, http.Header{}, "/api/x", "",
		targets))

	for i, ch := range []chan mirrorRequest{first, second} {
		select {
		case r := <-ch:
			if r.path != "/api/x" {
				t.Errorf("target %d: path want /api/x, got %q", i, r.path)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("target %d never received the request", i)
		}
	}
}

func TestNewModuleWithZeroValuesStillMirrors(t *testing.T) {
	got := make(chan mirrorRequest, 1)
	target := newMirrorServer(t, got)

	m, _ := newTestModule(t, 0, 0)

	if cap(m.queue) != defaultQueueSize {
		t.Errorf("queue capacity: want %d, got %d", defaultQueueSize, cap(m.queue))
	}

	m.TrySubmit(NewJob(http.MethodGet, nil, http.Header{}, "/api/x", "",
		[]MirrorTarget{{URL: target, Ratio: 1}}))

	select {
	case r := <-got:
		if r.path != "/api/x" {
			t.Errorf("path: want /api/x, got %q", r.path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no worker picked the job up")
	}
}

func TestStopIsIdempotentAndRejectsLateSubmits(t *testing.T) {
	got := make(chan mirrorRequest, 1)
	target := newMirrorServer(t, got)

	m, mc := newTestModule(t, 1, 1)
	m.Stop()
	m.Stop()

	m.TrySubmit(NewJob(http.MethodGet, nil, http.Header{}, "/late", "",
		[]MirrorTarget{{URL: target, Ratio: 1}}))

	if n := counterValue(t, mc, metrics.MirrorDropped); n != 1 {
		t.Errorf("dropped: want 1, got %v", n)
	}
}
