package loadbalancing

import (
	"errors"
	"fmt"
	"net/url"
	"sync"
)

const RoundRobinName = "round_robin"

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	Algorithm string `yaml:"algorithm"`
}

// Algorithm selects one backend for a key. Keys are upstream names, so each
// upstream rotates through its own backends independently.
type Algorithm interface {
	Pick(key string, targets []*url.URL) *url.URL
}

type LoadBalancer struct {
	algorithm Algorithm
}

func NewLoadBalancer(algorithm Algorithm) *LoadBalancer {
	return &LoadBalancer{algorithm: algorithm}
}

// Pick returns the backend selected by the configured algorithm. A nil
// balancer or algorithm, or an empty target list, falls back to the first
// target so a disabled feature keeps the old single-backend behavior.
func (b *LoadBalancer) Pick(key string, targets []*url.URL) *url.URL {
	if len(targets) == 0 {
		return nil
	}
	if b == nil || b.algorithm == nil {
		return targets[0]
	}
	return b.algorithm.Pick(key, targets)
}

func ValidateConfig(cfg Config) []error {
	algorithm := cfg.Algorithm
	if algorithm == "" {
		algorithm = RoundRobinName
	}

	switch algorithm {
	case RoundRobinName:
		return nil
	default:
		return []error{unknownAlgorithmError()}
	}
}

func unknownAlgorithmError() error {
	//nolint:staticcheck // the capitalization is part of the startup-facing contract
	return fmt.Errorf("Unknown algorithm. Supported: %q", RoundRobinName)
}

func New(cfg Config) (*LoadBalancer, error) {
	if errs := ValidateConfig(cfg); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	algorithm := cfg.Algorithm
	if algorithm == "" {
		algorithm = RoundRobinName
	}

	switch algorithm {
	case RoundRobinName:
		return NewLoadBalancer(newRoundRobin()), nil
	default:
		return nil, unknownAlgorithmError()
	}
}

type roundRobin struct {
	mu   sync.Mutex
	next map[string]uint64
}

func newRoundRobin() *roundRobin {
	return &roundRobin{next: make(map[string]uint64)}
}

func (r *roundRobin) Pick(key string, targets []*url.URL) *url.URL {
	if len(targets) == 0 {
		return nil
	}

	r.mu.Lock()
	n := r.next[key]
	r.next[key] = n + 1
	r.mu.Unlock()

	return targets[n%uint64(len(targets))]
}
