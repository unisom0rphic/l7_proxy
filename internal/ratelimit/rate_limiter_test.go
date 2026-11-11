package ratelimit

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestBurst(t *testing.T) {
	clock := newFakeClock()
	tb := newTokenBucket(10, 3, clock.Now)

	for i := 0; i < 3; i++ {
		if !tb.Allow("upstream") {
			t.Fatalf("request %d: want allowed, got denied", i)
		}
	}
	if tb.Allow("upstream") {
		t.Fatal("request after burst: want denied, got allowed")
	}
}

func TestRefill(t *testing.T) {
	clock := newFakeClock()
	tb := newTokenBucket(1, 2, clock.Now)

	for i := 0; i < 2; i++ {
		if !tb.Allow("upstream") {
			t.Fatalf("initial burst request %d: want allowed", i)
		}
	}
	if tb.Allow("upstream") {
		t.Fatal("empty bucket: want denied")
	}

	clock.Advance(time.Second)
	if !tb.Allow("upstream") {
		t.Fatal("after one second: want one token available")
	}
	if tb.Allow("upstream") {
		t.Fatal("after spending refilled token: want denied")
	}
}

func TestCapacity(t *testing.T) {
	clock := newFakeClock()
	tb := newTokenBucket(10, 2, clock.Now)

	for i := 0; i < 2; i++ {
		if !tb.Allow("upstream") {
			t.Fatalf("initial burst request %d: want allowed", i)
		}
	}

	clock.Advance(time.Hour)
	for i := 0; i < 2; i++ {
		if !tb.Allow("upstream") {
			t.Fatalf("request %d after long refill: want allowed", i)
		}
	}
	if tb.Allow("upstream") {
		t.Fatal("refill exceeded capacity: want denied")
	}
}

func TestKeyIsolation(t *testing.T) {
	clock := newFakeClock()
	tb := newTokenBucket(1, 1, clock.Now)

	if !tb.Allow("a") {
		t.Fatal("upstream a: want allowed")
	}
	if tb.Allow("a") {
		t.Fatal("upstream a: want denied")
	}
	if !tb.Allow("b") {
		t.Fatal("upstream b: want allowed")
	}
}

func TestValidateAccepts(t *testing.T) {
	errs := ValidateConfig(Config{
		Enabled:   true,
		Algorithm: TokenBucketName,
		TokenBucket: TokenBucketConfig{
			RefillRate: 5,
			BucketSize: 10,
		},
	})
	if len(errs) != 0 {
		t.Fatalf("want no errors, got: %v", errs)
	}
}

func TestValidateDefault(t *testing.T) {
	if errs := ValidateConfig(Config{}); len(errs) != 0 {
		t.Fatalf("want no errors, got: %v", errs)
	}
}

func TestValidateUnknown(t *testing.T) {
	errs := ValidateConfig(Config{Algorithm: "leaky_bucket"})
	if len(errs) != 1 {
		t.Fatalf("want 1 error, got %d: %v", len(errs), errs)
	}
	if got, want := errs[0].Error(), `Unknown algorithm. Supported: "token_bucket"`; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestValidateInvalid(t *testing.T) {
	tests := []struct {
		name   string
		config TokenBucketConfig
		want   string
	}{
		{"negative refill rate", TokenBucketConfig{RefillRate: -1}, "refill-rate must be >= 0"},
		{"negative bucket size", TokenBucketConfig{BucketSize: -1}, "bucket-size must be >= 0"},
		{"nan refill rate", TokenBucketConfig{RefillRate: math.NaN()}, "refill-rate must be finite"},
		{"infinite refill rate", TokenBucketConfig{RefillRate: math.Inf(1)}, "refill-rate must be finite"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateConfig(Config{
				Algorithm:   TokenBucketName,
				TokenBucket: tc.config,
			})
			if len(errs) != 1 {
				t.Fatalf("want 1 error, got %d: %v", len(errs), errs)
			}
			if !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("want error containing %q, got %q", tc.want, errs[0].Error())
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	limiter, err := New(Config{Algorithm: TokenBucketName})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < DefaultBucketSize; i++ {
		if !limiter.Allow("upstream") {
			t.Fatalf("request %d: want allowed", i)
		}
	}
	if limiter.Allow("upstream") {
		t.Fatal("request after default burst: want denied")
	}
}

func TestNewUnknown(t *testing.T) {
	_, err := New(Config{Algorithm: "leaky_bucket"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got, want := err.Error(), `Unknown algorithm. Supported: "token_bucket"`; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

type denyAllAlgorithm struct{}

func (denyAllAlgorithm) Allow(string) bool { return false }

func TestRateLimiterPluggable(t *testing.T) {
	var limiter interface {
		Allow(string) bool
	} = NewRateLimiter(denyAllAlgorithm{})

	if limiter.Allow("upstream") {
		t.Fatal("want fake algorithm to deny")
	}
}

func TestRateLimiterNil(t *testing.T) {
	var limiter *RateLimiter
	if !limiter.Allow("upstream") {
		t.Fatal("nil limiter: want allowed")
	}
}
