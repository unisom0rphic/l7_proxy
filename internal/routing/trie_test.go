package routing

import (
	"slices"
	"testing"

	"github.com/unisom0rphic/l7proxy/internal/config"
)

// yielded runs match to completion and returns every prefix offered, in order.
func yielded(tr *trie, path string) []string {
	var out []string
	tr.match(path, func(_ []*config.Route, depth int) bool {
		out = append(out, path[:depth])
		return false
	})
	return out
}

func assertYielded(t *testing.T, tr *trie, path string, want []string) {
	t.Helper()
	if got := yielded(tr, path); !slices.Equal(got, want) {
		t.Errorf("match(%q) yielded %v, want %v", path, got, want)
	}
}

func TestTrie_MatchOffersLongestPrefixFirst(t *testing.T) {
	tr := newTrie()
	for _, p := range []string{"/", "/api", "/api/v2", "/api/v2/users"} {
		tr.insert(p, &config.Route{Upstream: p})
	}

	assertYielded(t, tr, "/api/v2/users/42", []string{"/api/v2/users", "/api/v2", "/api", "/"})
}

func TestTrie_MatchYieldsOnlyRealPrefixes(t *testing.T) {
	tr := newTrie()
	for _, p := range []string{"/api", "/api/v2", "/other", "/api/v10"} {
		tr.insert(p, &config.Route{Upstream: p})
	}

	assertYielded(t, tr, "/api/v2/users", []string{"/api/v2", "/api"})
	assertYielded(t, tr, "/api/v1/users", []string{"/api"})
	assertYielded(t, tr, "/web/x", nil)
}

func TestTrie_MatchStopsAtFirstAccept(t *testing.T) {
	tr := newTrie()
	tr.insert("/", &config.Route{Upstream: "root"})
	tr.insert("/api", &config.Route{Upstream: "api"})

	var calls int
	tr.match("/api/users", func(routes []*config.Route, _ int) bool {
		calls++
		return routes[0].Upstream == "api"
	})
	if calls != 1 {
		t.Errorf("yield called %d times, want 1", calls)
	}
}

func TestTrie_SamePrefixKeepsInsertionOrder(t *testing.T) {
	tr := newTrie()
	tr.insert("/api", &config.Route{Upstream: "a"})
	tr.insert("/api", &config.Route{Upstream: "b"})

	var got []string
	tr.match("/api/users", func(routes []*config.Route, _ int) bool {
		for _, r := range routes {
			got = append(got, r.Upstream)
		}
		return true
	})
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("routes = %v, want %v", got, want)
	}
}

func TestTrie_PrefixLongerThanPathDoesNotMatch(t *testing.T) {
	tr := newTrie()
	tr.insert("/api/v2", &config.Route{Upstream: "api"})
	assertYielded(t, tr, "/api", nil)
}

func TestTrie_EmptyYieldsNothing(t *testing.T) {
	assertYielded(t, newTrie(), "/anything", nil)
}
