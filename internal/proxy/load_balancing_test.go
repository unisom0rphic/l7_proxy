package proxy

import (
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	loadbalancing "github.com/unisom0rphic/l7proxy/internal/load_balancing"
	"github.com/unisom0rphic/l7proxy/internal/routing"
)

func twoBackendDecision(t *testing.T, a, b string) routing.Decision {
	t.Helper()
	return routing.Decision{
		Upstream: "users",
		Target:   mustParse(t, a+"/api/x"),
		Targets:  []*url.URL{mustParse(t, a), mustParse(t, b)},
		Timeout:  2 * time.Second,
	}
}

// TestTransport_RoundRobinDistributesAcrossBackends drives the attempting
// transport with the real round-robin balancer and asserts that consecutive
// attempts against a multi-backend upstream alternate between its backends.
func TestTransport_RoundRobinDistributesAcrossBackends(t *testing.T) {
	a, aHits := newBackend(t, "a")
	b, bHits := newBackend(t, "b")

	lb, err := loadbalancing.New(loadbalancing.Config{Enabled: true})
	if err != nil {
		t.Fatalf("load balancing New: %v", err)
	}

	d := twoBackendDecision(t, a.URL, b.URL)

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})
	tr.p.balancer = lb

	for i := 0; i < 4; i++ {
		resp, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true))
		if err != nil {
			t.Fatalf("RoundTrip %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		want := "a:/api/x"
		if i%2 == 1 {
			want = "b:/api/x"
		}
		if string(body) != want {
			t.Errorf("pick %d: want %q, got %q", i, want, body)
		}
	}

	if got := aHits.Load(); got != 2 {
		t.Errorf("backend a hits: want 2, got %d", got)
	}
	if got := bHits.Load(); got != 2 {
		t.Errorf("backend b hits: want 2, got %d", got)
	}
}

// TestTransport_BalancerDisabledKeepsFirstTarget checks the disabled path: a
// proxy without a balancer sends every attempt to the first backend, matching
// the single-backend behavior of an upstream declared with host.
func TestTransport_BalancerDisabledKeepsFirstTarget(t *testing.T) {
	a, aHits := newBackend(t, "a")
	b, bHits := newBackend(t, "b")

	d := twoBackendDecision(t, a.URL, b.URL)

	tr := newTestTransport(&fakeGate{}, &fakeLimiter{})

	for i := 0; i < 3; i++ {
		resp, err := tr.RoundTrip(decisionRequest(t, http.MethodGet, nil, d, true))
		if err != nil {
			t.Fatalf("RoundTrip %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if string(body) != "a:/api/x" {
			t.Errorf("pick %d: want the first backend, got %q", i, body)
		}
	}

	if got := aHits.Load(); got != 3 {
		t.Errorf("backend a hits: want 3, got %d", got)
	}
	if got := bHits.Load(); got != 0 {
		t.Errorf("backend b hits: want 0, got %d", got)
	}
}
