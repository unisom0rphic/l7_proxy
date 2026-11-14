package loadbalancing

import (
	"net/url"
	"strings"
	"testing"
)

func mustTargets(t *testing.T, raws ...string) []*url.URL {
	t.Helper()
	targets := make([]*url.URL, 0, len(raws))
	for _, raw := range raws {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		targets = append(targets, u)
	}
	return targets
}

func TestRoundRobinCycles(t *testing.T) {
	lb, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	targets := mustTargets(t,
		"http://a:1111",
		"http://b:2222",
		"http://c:3333",
	)
	want := []string{
		"http://a:1111",
		"http://b:2222",
		"http://c:3333",
		"http://a:1111",
		"http://b:2222",
	}

	for i, expected := range want {
		if got := lb.Pick("users", targets); got.String() != expected {
			t.Fatalf("pick %d: want %q, got %q", i, expected, got)
		}
	}
}

func TestRoundRobinKeyIsolation(t *testing.T) {
	lb, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	targetsA := mustTargets(t, "http://a1:1", "http://a2:2")
	targetsB := mustTargets(t, "http://b1:1", "http://b2:2")

	if got := lb.Pick("a", targetsA); got.String() != "http://a1:1" {
		t.Fatalf("a first pick: got %q", got)
	}
	if got := lb.Pick("b", targetsB); got.String() != "http://b1:1" {
		t.Fatalf("b first pick must not advance a: got %q", got)
	}
	if got := lb.Pick("a", targetsA); got.String() != "http://a2:2" {
		t.Fatalf("a second pick: got %q", got)
	}
}

func TestRoundRobinSingleAndEmptyTargets(t *testing.T) {
	lb, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	one := mustTargets(t, "http://only:1111")
	for i := 0; i < 3; i++ {
		if got := lb.Pick("a", one); got.String() != "http://only:1111" {
			t.Fatalf("single target pick %d: got %q", i, got)
		}
	}

	if got := lb.Pick("a", nil); got != nil {
		t.Fatalf("empty targets: want nil, got %q", got)
	}
}

func TestNilLoadBalancerFallsBackToFirst(t *testing.T) {
	var lb *LoadBalancer
	targets := mustTargets(t, "http://a:1111", "http://b:2222")

	if got := lb.Pick("a", targets); got.String() != "http://a:1111" {
		t.Fatalf("nil balancer: want first target, got %q", got)
	}
	if got := lb.Pick("a", nil); got != nil {
		t.Fatalf("nil balancer with no targets: want nil, got %q", got)
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"default algorithm", Config{}, ""},
		{"explicit round robin", Config{Enabled: true, Algorithm: RoundRobinName}, ""},
		{"unknown algorithm", Config{Algorithm: "least_conn"}, `Unknown algorithm. Supported: "round_robin"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateConfig(tc.cfg)
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatalf("want no errors, got: %v", errs)
				}
				return
			}
			if len(errs) != 1 {
				t.Fatalf("want 1 error, got %d: %v", len(errs), errs)
			}
			if !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("want error containing %q, got %q", tc.want, errs[0].Error())
			}
		})
	}
}

func TestNewUnknownAlgorithm(t *testing.T) {
	_, err := New(Config{Algorithm: "least_conn"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "Unknown algorithm") {
		t.Fatalf("unexpected error: %v", err)
	}
}
