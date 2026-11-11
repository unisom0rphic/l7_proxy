package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unisom0rphic/l7proxy/internal/routing"
)

// fakeGate is a Gate with a fixed verdict per upstream that also remembers the
// outcomes it was asked to record.
type fakeGate struct {
	deny    map[string]bool
	records map[string][]error
}

func (g *fakeGate) Allow(upstream string) bool { return !g.deny[upstream] }

func (g *fakeGate) Record(upstream string, err error) {
	if g.records != nil {
		g.records[upstream] = append(g.records[upstream], err)
	}
}

type fakeLimiter struct{ deny map[string]bool }

func (l *fakeLimiter) Allow(upstream string) bool { return !l.deny[upstream] }

func newTestTransport(gate Gate, limiter Limiter) *attemptingTransport {
	return &attemptingTransport{
		base: &http.Transport{},
		p:    &L7Proxy{gate: gate, limiter: limiter},
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// bindAndClose returns a URL whose port is guaranteed to refuse connections.
func deadURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return "http://" + addr
}

// newBackend answers "<name>:<path>" and counts the requests it served.
func newBackend(t *testing.T, name string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, name+":"+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// decisionRequest builds the outbound request as reverseProxy hands it to the
// transport: the decision rides in the context, the target is the outbound URL
// and GetBody is present only when the body can be replayed.
func decisionRequest(t *testing.T, method string, body []byte, d routing.Decision, replayable bool) *http.Request {
	t.Helper()
	req := &http.Request{
		Method:        method,
		URL:           d.Target,
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	if replayable {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	return req.WithContext(context.WithValue(context.Background(), decisionKey{}, d))
}

func TestTransport_FailsOverOnRefusedPrimary(t *testing.T) {
	fb, fbHits := newBackend(t, "fb")

	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, deadURL(t)+"/api/x"),
		Timeout:  2 * time.Second,
		Fallback: &routing.FailoverTarget{Name: "fb", URL: mustParse(t, fb.URL), Timeout: 2 * time.Second},
	}

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})
	resp, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "fb:/api/x" {
		t.Errorf("want 200 fb:/api/x, got %d %q", resp.StatusCode, body)
	}
	if got := fbHits.Load(); got != 1 {
		t.Errorf("fallback hits: want 1, got %d", got)
	}
}

func TestTransport_NoFailoverForNonIdempotent(t *testing.T) {
	fb, fbHits := newBackend(t, "fb")

	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, deadURL(t)+"/api/x"),
		Timeout:  2 * time.Second,
		Fallback: &routing.FailoverTarget{Name: "fb", URL: mustParse(t, fb.URL), Timeout: 2 * time.Second},
	}

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})
	if _, err := tr.RoundTrip(decisionRequest(t, http.MethodPost, []byte("hi"), d, true)); err == nil {
		t.Fatal("POST: want error, got nil")
	}
	if got := fbHits.Load(); got != 0 {
		t.Errorf("fallback must not be tried for POST, got %d hits", got)
	}
}

func TestTransport_NoFallbackConfigured(t *testing.T) {
	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, deadURL(t)+"/api/x"),
		Timeout:  2 * time.Second,
	}

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})
	if _, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true)); err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestTransport_NoFailoverWhenBodyNotReplayable(t *testing.T) {
	fb, fbHits := newBackend(t, "fb")

	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, deadURL(t)+"/api/x"),
		Timeout:  2 * time.Second,
		Fallback: &routing.FailoverTarget{Name: "fb", URL: mustParse(t, fb.URL), Timeout: 2 * time.Second},
	}

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})
	if _, err := tr.RoundTrip(decisionRequest(t, http.MethodPut, []byte("payload"), d, false)); err == nil {
		t.Fatal("PUT without GetBody: want error, got nil")
	}
	if got := fbHits.Load(); got != 0 {
		t.Errorf("fallback must not be tried when the body cannot be replayed, got %d hits", got)
	}
}

func TestTransport_PerAttemptTimeoutFailsOver(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(slow.Close)

	fb, _ := newBackend(t, "fb")

	d := routing.Decision{
		Upstream: "slow",
		Target:   mustParse(t, slow.URL+"/api/x"),
		Timeout:  50 * time.Millisecond,
		Fallback: &routing.FailoverTarget{Name: "fb", URL: mustParse(t, fb.URL), Timeout: 2 * time.Second},
	}

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})
	start := time.Now()
	resp, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fb:/api/x" {
		t.Errorf("want fallback body, got %q", body)
	}
	if elapsed >= time.Second {
		t.Errorf("fallback took %s: the primary's per-attempt timeout did not fire", elapsed)
	}
}

func TestTransport_GateDenialFailsOver(t *testing.T) {
	primary, primaryHits := newBackend(t, "primary")
	fb, fbHits := newBackend(t, "fb")

	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, primary.URL+"/api/x"),
		Timeout:  2 * time.Second,
		Fallback: &routing.FailoverTarget{Name: "fb", URL: mustParse(t, fb.URL), Timeout: 2 * time.Second},
	}

	gate := &fakeGate{deny: map[string]bool{"primary": true}}
	tr := newTestTransport(gate, &fakeLimiter{})

	resp, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fb:/api/x" {
		t.Errorf("want fallback body, got %q", body)
	}
	if got := primaryHits.Load(); got != 0 {
		t.Errorf("primary must not be contacted while the gate is closed, got %d hits", got)
	}
	if got := fbHits.Load(); got != 1 {
		t.Errorf("fallback hits: want 1, got %d", got)
	}
}

func TestTransport_AllUpstreamsUnavailable(t *testing.T) {
	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, "http://127.0.0.1:1/api/x"),
		Timeout:  time.Second,
		Fallback: &routing.FailoverTarget{Name: "fb", URL: mustParse(t, "http://127.0.0.1:1/api/x"), Timeout: time.Second},
	}

	gate := &fakeGate{deny: map[string]bool{"primary": true, "fb": true}}
	tr := newTestTransport(gate, &fakeLimiter{})

	if _, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true)); !errors.Is(err, errUpstreamUnavailable) {
		t.Fatalf("want errUpstreamUnavailable, got %v", err)
	}
}

func TestTransport_LimiterDenialIsRateLimited(t *testing.T) {
	d := routing.Decision{
		Upstream: "primary",
		Target:   mustParse(t, "http://127.0.0.1:1/api/x"),
		Timeout:  time.Second,
	}

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{deny: map[string]bool{"primary": true}})
	if _, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true)); !errors.Is(err, errRateLimited) {
		t.Fatalf("want errRateLimited, got %v", err)
	}
}

func TestTransport_RecordsOutcome(t *testing.T) {
	ok, _ := newBackend(t, "ok")
	gate := &fakeGate{records: map[string][]error{}}
	tr := newTestTransport(gate, &fakeLimiter{})

	up := routing.Decision{Upstream: "primary", Target: mustParse(t, ok.URL+"/api/x"), Timeout: time.Second}
	resp, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, up, true))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if recs := gate.records["primary"]; len(recs) != 1 || recs[0] != nil {
		t.Fatalf("success: want a single nil record, got %v", recs)
	}

	down := routing.Decision{Upstream: "primary", Target: mustParse(t, deadURL(t)+"/api/x"), Timeout: time.Second}
	if _, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, down, true)); err == nil {
		t.Fatal("want error from the unreachable primary")
	}
	if recs := gate.records["primary"]; len(recs) != 2 || recs[1] == nil {
		t.Fatalf("failure: want a non-nil record, got %v", recs)
	}
}

func TestErrorHandler_StatusMapping(t *testing.T) {
	p := &L7Proxy{}

	tests := []struct {
		name string
		err  error
		want int
	}{
		{"rate limited", errRateLimited, http.StatusTooManyRequests},
		{"upstream unavailable", errUpstreamUnavailable, http.StatusServiceUnavailable},
		{"deadline exceeded", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"body too large", &http.MaxBytesError{Limit: 1}, http.StatusRequestEntityTooLarge},
		{"generic transport error", errors.New("boom"), http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			p.errorHandler(rec, httptest.NewRequest(http.MethodGet, "/x", nil), tc.err)
			if rec.Code != tc.want {
				t.Errorf("want %d, got %d", tc.want, rec.Code)
			}
		})
	}
}

func TestRewrite_BuffersBodyForReplay(t *testing.T) {
	p := &L7Proxy{}
	target := mustParse(t, "http://up:1/api/x")
	d := routing.Decision{Upstream: "up", Target: target, Timeout: time.Second}

	in := httptest.NewRequest(http.MethodPut, "/api/x", strings.NewReader("payload"))
	in = in.WithContext(context.WithValue(in.Context(), decisionKey{}, d))
	out := in.Clone(in.Context())

	p.rewrite(&httputil.ProxyRequest{In: in, Out: out})

	if out.GetBody == nil {
		t.Fatal("GetBody: want a replay function, got nil")
	}
	rc, err := out.GetBody()
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if string(got) != "payload" {
		t.Errorf("replayed body: want %q, got %q", "payload", got)
	}
	if out.URL.String() != target.String() {
		t.Errorf("out URL: want %q, got %q", target, out.URL)
	}
}
