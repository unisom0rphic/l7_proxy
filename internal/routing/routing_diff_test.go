package routing

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/unisom0rphic/l7proxy/internal/config"
)

// referenceDecide is the oracle for the trie-backed index. It scans cfg.Routes
// directly and applies the documented precedence: an exact path match, else the
// longest prefix, else the first regex in config order.
func referenceDecide(cfg *config.Config, r *http.Request) (*config.Route, bool) {
	path := r.URL.Path

	var exact, prefix, regex *config.Route
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		rule := route.Rule
		switch {
		case rule.Path != "":
			if exact == nil && rule.Path == path && matchConditions(rule, r) {
				exact = route
			}
		case rule.PathPrefix != "":
			if !strings.HasPrefix(path, rule.PathPrefix) || !matchConditions(rule, r) {
				continue
			}
			if prefix == nil || len(rule.PathPrefix) > len(prefix.Rule.PathPrefix) {
				prefix = route
			}
		case rule.Regex != "":
			if regex != nil || !matchConditions(rule, r) {
				continue
			}
			if re := rule.CompiledRegex(); re != nil && re.MatchString(path) {
				regex = route
			}
		}
	}

	for _, c := range []*config.Route{exact, prefix, regex} {
		if c != nil {
			return c, true
		}
	}
	return nil, false
}

var (
	diffSegments = []string{"api", "v1", "v2", "admin", "users", "orders", "items", "42", "public", "internal"}
	diffMethods  = []string{"", "GET", "POST", "PUT"}
	diffHeaders  = []string{"", "x-trace", "x-env"}
	diffHosts    = []string{"", "example.com", "*.example.com"}
)

func diffPaths(rng *rand.Rand, n int) []string {
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	for len(out) < n {
		depth := 1 + rng.Intn(4)
		parts := make([]string, depth)
		for i := range parts {
			parts[i] = diffSegments[rng.Intn(len(diffSegments))]
		}
		p := "/" + strings.Join(parts, "/")
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// diffPrefixOf cuts path at a segment boundary, yielding a prefix that is not
// necessarily a registered route.
func diffPrefixOf(rng *rand.Rand, path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return "/" + strings.Join(parts[:1+rng.Intn(len(parts))], "/")
}

// diffConfigYAML renders a random config and returns the path pool its rules
// were derived from, so callers can build requests that actually hit them.
func diffConfigYAML(rng *rand.Rand) (string, []string) {
	var b strings.Builder

	upstreamCount := 1 + rng.Intn(3)
	b.WriteString("upstreams:\n")
	for i := 0; i < upstreamCount; i++ {
		fmt.Fprintf(&b, "  - name: \"u%d\"\n    host: \"http://u%d:9000\"\n    timeout-ms: 300\n", i, i)
	}

	paths := diffPaths(rng, 12)
	b.WriteString("routes:\n")
	for i := 0; i < 8+rng.Intn(16); i++ {
		fmt.Fprintf(&b, "  - upstream: \"u%d\"\n    rules:\n", rng.Intn(upstreamCount))
		switch rng.Intn(3) {
		case 0:
			fmt.Fprintf(&b, "      path: \"%s\"\n", paths[rng.Intn(len(paths))])
		case 1:
			fmt.Fprintf(&b, "      path-prefix: \"%s\"\n", diffPrefixOf(rng, paths[rng.Intn(len(paths))]))
		default:
			fmt.Fprintf(&b, "      regex: \"^%s(/.*)?$\"\n",
				regexp.QuoteMeta(diffPrefixOf(rng, paths[rng.Intn(len(paths))])))
		}
		if m := diffMethods[rng.Intn(len(diffMethods))]; m != "" {
			fmt.Fprintf(&b, "      method: \"%s\"\n", m)
		}
		if h := diffHeaders[rng.Intn(len(diffHeaders))]; h != "" {
			fmt.Fprintf(&b, "      headers:\n        - name: \"%s\"\n", h)
		}
		if ho := diffHosts[rng.Intn(len(diffHosts))]; ho != "" {
			fmt.Fprintf(&b, "      hosts:\n        - \"%s\"\n", ho)
		}
	}
	return b.String(), paths
}

func diffRequest(rng *rand.Rand, paths []string) *http.Request {
	path := paths[rng.Intn(len(paths))]
	if rng.Intn(4) == 0 {
		path += "/" + diffSegments[rng.Intn(len(diffSegments))]
	}

	hdr := make(http.Header)
	for _, h := range []string{"x-trace", "x-env"} {
		if rng.Intn(2) == 0 {
			hdr.Set(h, "1")
		}
	}

	return &http.Request{
		Method: []string{"GET", "POST", "PUT"}[rng.Intn(3)],
		URL:    &url.URL{Path: path},
		Header: hdr,
		Host:   []string{"", "example.com", "sub.example.com", "other.net"}[rng.Intn(4)],
	}
}

func TestDecideRoute_MatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261030))
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	var hits, misses int
	for iter := 0; iter < 100; iter++ {
		yaml, routePaths := diffConfigYAML(rng)
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		cfg, err := config.ParseConfig(cfgPath)
		if err != nil {
			t.Fatalf("iter %d: parse generated config: %v\n%s", iter, err, yaml)
		}
		router, err := NewRouter(cfg)
		if err != nil {
			t.Fatalf("iter %d: NewRouter: %v", iter, err)
		}

		// Mostly draw from the paths the rules were built from, so matches are
		// common; a few fresh paths keep misses in the mix.
		paths := diffPaths(rng, 10)
		for i := 0; i < 3; i++ {
			paths = append(paths, routePaths...)
		}
		for q := 0; q < 100; q++ {
			req := diffRequest(rng, paths)
			want, wantOK := referenceDecide(cfg, req)
			got, gotErr := router.DecideRoute(req)

			if wantOK != (gotErr == nil) {
				t.Fatalf("iter %d req %s %s: reference matched=%v, DecideRoute err=%v\n%s",
					iter, req.Method, req.URL.Path, wantOK, gotErr, yaml)
			}
			if wantOK {
				hits++
			} else {
				misses++
			}
			if wantOK && got.Upstream != want.Upstream {
				t.Fatalf("iter %d req %s %s (host %q): reference picked %q, DecideRoute picked %q\n%s",
					iter, req.Method, req.URL.Path, req.Host, want.Upstream, got.Upstream, yaml)
			}
		}
	}

	if hits == 0 || misses == 0 {
		t.Fatalf("differential run is not meaningful: %d hits, %d misses", hits, misses)
	}
	t.Logf("compared %d decisions (%d matched)", hits+misses, hits)
}
