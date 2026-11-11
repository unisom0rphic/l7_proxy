package ratelimit

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	TokenBucketName   = "token_bucket"
	DefaultRefillRate = 10.0
	DefaultBucketSize = 20
)

type Config struct {
	Enabled     bool              `yaml:"enabled"`
	Algorithm   string            `yaml:"algorithm"`
	TokenBucket TokenBucketConfig `yaml:"token-bucket"`
}

type TokenBucketConfig struct {
	RefillRate float64 `yaml:"refill-rate"`
	BucketSize int     `yaml:"bucket-size"`
}

type RateLimitAlgorithm interface {
	Allow(key string) bool
}

type RateLimiter struct {
	algorithm RateLimitAlgorithm
}

func NewRateLimiter(algorithm RateLimitAlgorithm) *RateLimiter {
	return &RateLimiter{algorithm: algorithm}
}

func (r *RateLimiter) Allow(key string) bool {
	if r == nil || r.algorithm == nil {
		return true
	}
	return r.algorithm.Allow(key)
}

func ValidateConfig(cfg Config) []error {
	var errs []error

	algorithm := cfg.Algorithm
	if algorithm == "" {
		algorithm = TokenBucketName
	}

	switch algorithm {
	case TokenBucketName:
		tb := cfg.TokenBucket.withDefaults()
		switch {
		case math.IsNaN(tb.RefillRate), math.IsInf(tb.RefillRate, 0):
			errs = append(errs, fmt.Errorf(
				"rate-limit.token-bucket.refill-rate must be finite, got %v", tb.RefillRate))
		case tb.RefillRate < 0:
			errs = append(errs, fmt.Errorf(
				"rate-limit.token-bucket.refill-rate must be >= 0, got %v", tb.RefillRate))
		}
		if tb.BucketSize < 0 {
			errs = append(errs, fmt.Errorf(
				"rate-limit.token-bucket.bucket-size must be >= 0, got %d", tb.BucketSize))
		}
	default:
		errs = append(errs, unknownAlgorithmError())
	}

	return errs
}

func unknownAlgorithmError() error {
	//nolint:staticcheck // the capitalization is part of the startup-facing contract
	return fmt.Errorf("Unknown algorithm. Supported: %q", TokenBucketName)
}

func New(cfg Config) (*RateLimiter, error) {
	if errs := ValidateConfig(cfg); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	algorithm := cfg.Algorithm
	if algorithm == "" {
		algorithm = TokenBucketName
	}

	switch algorithm {
	case TokenBucketName:
		tb := cfg.TokenBucket.withDefaults()
		return NewRateLimiter(newTokenBucket(tb.RefillRate, tb.BucketSize, time.Now)), nil
	default:
		return nil, unknownAlgorithmError()
	}
}

func (c TokenBucketConfig) withDefaults() TokenBucketConfig {
	if c.RefillRate == 0 {
		c.RefillRate = DefaultRefillRate
	}
	if c.BucketSize == 0 {
		c.BucketSize = DefaultBucketSize
	}
	return c
}

type tokenBucket struct {
	mu         sync.Mutex
	refillRate float64
	capacity   float64
	now        func() time.Time
	buckets    map[string]*bucketState
}

type bucketState struct {
	tokens float64
	last   time.Time
}

func newTokenBucket(refillRate float64, bucketSize int, now func() time.Time) *tokenBucket {
	if now == nil {
		now = time.Now
	}
	return &tokenBucket{
		refillRate: refillRate,
		capacity:   float64(bucketSize),
		now:        now,
		buckets:    make(map[string]*bucketState),
	}
}

func (t *tokenBucket) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	b, ok := t.buckets[key]
	if !ok {
		b = &bucketState{tokens: t.capacity, last: now}
		t.buckets[key] = b
	}

	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(t.capacity, b.tokens+elapsed*t.refillRate)
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
