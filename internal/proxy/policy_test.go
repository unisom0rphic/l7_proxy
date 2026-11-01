package proxy

import (
	"errors"
	"testing"
)

func TestAllowAllGate_AdmitsEverything(t *testing.T) {
	var g Gate = allowAllGate{}

	for _, name := range []string{"", "a", "users-prod"} {
		if !g.Allow(name) {
			t.Errorf("Allow(%q): want true, got false", name)
		}
	}

	g.Record("a", errors.New("boom"))
	if !g.Allow("a") {
		t.Error("Allow after a recorded failure: want true, got false")
	}
}

func TestUnlimitedLimiter_AdmitsEverything(t *testing.T) {
	var l Limiter = unlimitedLimiter{}

	for _, name := range []string{"", "a", "users-prod"} {
		if !l.Allow(name) {
			t.Errorf("Allow(%q): want true, got false", name)
		}
	}
}
