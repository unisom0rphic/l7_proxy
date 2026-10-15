package routing

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unisom0rphic/l7proxy/internal/config"
)

func testConfig(upstreams []config.Upstream, routes []config.Route) *config.Config {
	cfg := &config.Config{
		Upstreams: upstreams,
		Routes:    routes,
	}

	cfg.NameToHost = make(map[string]*url.URL, len(upstreams))
	for _, up := range upstreams {
		u, err := url.Parse(up.Host)
		if err != nil {
			panic("bad upstream host " + up.Host + ": " + err.Error())
		}
		cfg.NameToHost[up.Name] = u
	}

	return cfg
}

func upstream(name, host string) config.Upstream {
	return config.Upstream{Name: name, Host: host, Timeout: 300}
}

func prefixRoute(prefix, upstreamName string) config.Route {
	return config.Route{Rule: config.Rule{PathPrefix: prefix}, Upstream: upstreamName}
}

func newTestRouter(t *testing.T, cfg *config.Config) *Router {
	t.Helper()
	r, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func request(t *testing.T, method, target string, hdr http.Header) *http.Request {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("bad target %q: %v", target, err)
	}
	if method == "" {
		method = http.MethodGet
	}
	return &http.Request{Method: method, URL: u, Header: hdr}
}

func mustDecide(t *testing.T, r *Router, req *http.Request) string {
	t.Helper()
	d, err := r.DecideRoute(req)
	if err != nil {
		t.Fatalf("DecideRoute(%s %s): unexpected error: %v", req.Method, req.URL.Path, err)
	}
	if d.Target == nil {
		t.Fatalf("DecideRoute(%s %s): nil Target without error (contract violation)", req.Method, req.URL.Path)
	}
	return d.Target.String()
}

func noDecide(t *testing.T, r *Router, req *http.Request) {
	t.Helper()
	d, err := r.DecideRoute(req)
	if err == nil {
		t.Fatalf("DecideRoute(%s %s): expected miss, got %q", req.Method, req.URL.Path, d.Target)
	}
	if d.Target != nil {
		t.Fatalf("DecideRoute(%s %s): error present but Target is not nil: %q", req.Method, req.URL.Path, d.Target)
	}
}

func TestDecideRoute_PrefixPassthrough(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("test", "http://testnet:3000")},
		[]config.Route{prefixRoute("/api/test", "test")},
	))

	cases := []struct{ path, want string }{
		{"/api/test", "http://testnet:3000/api/test"},
		{"/api/test/users/42", "http://testnet:3000/api/test/users/42"},
	}
	for _, tc := range cases {
		if got := mustDecide(t, r, request(t, "GET", tc.path, nil)); got != tc.want {
			t.Errorf("GET %s: want %q, got %q", tc.path, tc.want, got)
		}
	}
}

func TestDecideRoute_LongestPrefixWins(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:11111"), upstream("b", "http://b:2222")},
		[]config.Route{prefixRoute("/api", "a"), prefixRoute("/api/v2", "b")},
	))

	cases := []struct{ path, want string }{
		{"/api/v2/users", "http://a:11111/api/v2/users"},
		{"/api/v2", "http://a:11111/api/v2"},
		{"/api/other", "http://a:11111/api/other"},
		{"/api", "http://a:11111/api"},
	}
	for _, tc := range cases {
		if got := mustDecide(t, r, request(t, "GET", tc.path, nil)); got != tc.want {
			t.Errorf("GET %s: want %q, got %q", tc.path, tc.want, got)
		}
	}
}

func TestDecideRoute_NoMatch(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{prefixRoute("/api", "a")},
	))
	noDecide(t, r, request(t, "GET", "/nope", nil))
}

func TestDecideRoute_NilURL(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{prefixRoute("/api", "a")},
	))
	d, err := r.DecideRoute(&http.Request{})
	if err == nil {
		t.Fatalf("expected error for request without URL, got %q", d.Target)
	}
}

func TestDecideRoute_MethodFilter(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{
			{Rule: config.Rule{PathPrefix: "/post-only", Method: "POST"}, Upstream: "a"},
			{Rule: config.Rule{PathPrefix: "/any"}, Upstream: "a"},
		},
	))

	mustDecide(t, r, request(t, http.MethodPost, "/post-only", nil))
	noDecide(t, r, request(t, http.MethodGet, "/post-only", nil))
	noDecide(t, r, request(t, http.MethodPut, "/post-only", nil))

	mustDecide(t, r, request(t, http.MethodGet, "/any", nil))
	mustDecide(t, r, request(t, http.MethodPost, "/any", nil))
}

func TestDecideRoute_HeaderPresence(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{{
			Rule: config.Rule{
				PathPrefix: "/api",
				Headers:    []config.HeaderMatch{{Name: "X-Api-Version", Op: config.HeaderPresent}},
			},
			Upstream: "a",
		}},
	))

	h := http.Header{}
	h.Set("X-Api-Version", "whatever-value")
	mustDecide(t, r, request(t, "GET", "/api/users", h))
	noDecide(t, r, request(t, "GET", "/api/users", nil))
}

func TestDecideRoute_HeaderExactValue(t *testing.T) {
	newR := func(match config.HeaderMatch) *Router {
		return newTestRouter(t, testConfig(
			[]config.Upstream{upstream("a", "http://a:1111")},
			[]config.Route{{
				Rule: config.Rule{
					PathPrefix: "/api",
					Headers:    []config.HeaderMatch{match},
				},
				Upstream: "a",
			}},
		))
	}

	t.Run("equals op", func(t *testing.T) {
		r := newR(config.HeaderMatch{Name: "X-Api-Version", Value: "2", Op: config.HeaderEquals})

		t.Run("exact value matches", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "2")
			mustDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("different value rejects", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "3")
			noDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("missing header rejects", func(t *testing.T) {
			noDecide(t, r, request(t, "GET", "/api/users", nil))
		})

		t.Run("value comparison is case sensitive", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "V2")
			noDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("any of multiple values matches", func(t *testing.T) {
			h := http.Header{}
			h.Add("X-Api-Version", "1")
			h.Add("X-Api-Version", "2")
			mustDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("config header name is case insensitive", func(t *testing.T) {
			// Config uses lowercase; runtime header lands under the canonical
			// key. http.Header.Values canonicalizes the lookup key, so this
			// must still match.
			r := newR(config.HeaderMatch{Name: "x-api-version", Value: "2", Op: config.HeaderEquals})
			h := http.Header{}
			h.Set("X-Api-Version", "2")
			mustDecide(t, r, request(t, "GET", "/api/users", h))
		})
	})

	// Shorthand: empty Op + non-empty Value defaults to equals.
	t.Run("shorthand defaults to equals", func(t *testing.T) {
		r := newR(config.HeaderMatch{Name: "X-Api-Version", Value: "2"})

		t.Run("matching value", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "2")
			mustDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("different value rejects", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "3")
			noDecide(t, r, request(t, "GET", "/api/users", h))
		})
	})

	// Two equals rules on the same route = AND.
	t.Run("multiple header rules are ANDed", func(t *testing.T) {
		r := newTestRouter(t, testConfig(
			[]config.Upstream{upstream("a", "http://a:1111")},
			[]config.Route{{
				Rule: config.Rule{
					PathPrefix: "/api",
					Headers: []config.HeaderMatch{
						{Name: "X-Api-Version", Value: "2", Op: config.HeaderEquals},
						{Name: "X-Tenant", Value: "acme", Op: config.HeaderEquals},
					},
				},
				Upstream: "a",
			}},
		))

		t.Run("both present and matching", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "2")
			h.Set("X-Tenant", "acme")
			mustDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("one missing rejects", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "2")
			noDecide(t, r, request(t, "GET", "/api/users", h))
		})

		t.Run("one wrong value rejects", func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Api-Version", "2")
			h.Set("X-Tenant", "other")
			noDecide(t, r, request(t, "GET", "/api/users", h))
		})
	})
}

func TestDecideRoute_HeaderPrefix(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{{
			Rule: config.Rule{
				PathPrefix: "/api",
				Headers: []config.HeaderMatch{
					{Name: "User-Agent", Value: "mobile-", Op: config.HeaderPrefix},
				},
			},
			Upstream: "a",
		}},
	))

	t.Run("prefix matches", func(t *testing.T) {
		h := http.Header{}
		h.Set("User-Agent", "mobile-ios/1.2.3")
		mustDecide(t, r, request(t, "GET", "/api/x", h))
	})

	t.Run("exact value is a prefix of itself", func(t *testing.T) {
		h := http.Header{}
		h.Set("User-Agent", "mobile-")
		mustDecide(t, r, request(t, "GET", "/api/x", h))
	})

	t.Run("non-matching prefix rejects", func(t *testing.T) {
		h := http.Header{}
		h.Set("User-Agent", "desktop-chrome/120")
		noDecide(t, r, request(t, "GET", "/api/x", h))
	})

	t.Run("empty prefix matches any present value", func(t *testing.T) {
		r := newTestRouter(t, testConfig(
			[]config.Upstream{upstream("a", "http://a:1111")},
			[]config.Route{{
				Rule: config.Rule{
					PathPrefix: "/api",
					Headers: []config.HeaderMatch{
						{Name: "X-Anything", Value: "", Op: config.HeaderPrefix},
					},
				},
				Upstream: "a",
			}},
		))
		h := http.Header{}
		h.Set("X-Anything", "whatever")
		mustDecide(t, r, request(t, "GET", "/api/x", h))
		noDecide(t, r, request(t, "GET", "/api/x", nil))
	})
}

func TestDecideRoute_HeaderContains(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{{
			Rule: config.Rule{
				PathPrefix: "/api",
				Headers: []config.HeaderMatch{
					{Name: "X-Features", Value: "beta", Op: config.HeaderContains},
				},
			},
			Upstream: "a",
		}},
	))

	t.Run("substring matches", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Features", "alpha,beta,gamma")
		mustDecide(t, r, request(t, "GET", "/api/x", h))
	})

	t.Run("non-matching substring rejects", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Features", "alpha,gamma")
		noDecide(t, r, request(t, "GET", "/api/x", h))
	})

	t.Run("matches any of multiple values", func(t *testing.T) {
		h := http.Header{}
		h.Add("X-Features", "alpha")
		h.Add("X-Features", "beta-tester")
		mustDecide(t, r, request(t, "GET", "/api/x", h))
	})
}

func TestDecideRoute_ExactPath(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{{Rule: config.Rule{Path: "/health"}, Upstream: "a"}},
	))

	mustDecide(t, r, request(t, "GET", "/health", nil))
	noDecide(t, r, request(t, "GET", "/health/x", nil))
	noDecide(t, r, request(t, "GET", "/healt", nil))
}

func TestDecideRoute_CombinedFilters(t *testing.T) {
	h := http.Header{}
	h.Set("X-Debug", "1")

	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{{
			Rule: config.Rule{
				PathPrefix: "/api",
				Method:     "POST",
				Headers:    []config.HeaderMatch{{Name: "X-Debug", Op: config.HeaderPresent}},
			},
			Upstream: "a",
		}},
	))

	mustDecide(t, r, request(t, "POST", "/api/x", h))
	noDecide(t, r, request(t, "GET", "/api/x", h))
	noDecide(t, r, request(t, "POST", "/api/x", nil))
}

func TestDecideRoute_PrefixEdgeCases(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{prefixRoute("/api", "a")},
	))

	t.Run("trailing slash matches by prefix", func(t *testing.T) {
		if got := mustDecide(t, r, request(t, "GET", "/api/", nil)); got != "http://a:1111/api/" {
			t.Errorf("want %q, got %q", "http://a:1111/api/", got)
		}
	})

	// No segment bounds: /api matches /apifoo.
	t.Run("match inside segment — documented behavior", func(t *testing.T) {
		if got := mustDecide(t, r, request(t, "GET", "/apifoo", nil)); got != "http://a:1111/apifoo" {
			t.Errorf("want %q, got %q", "http://a:1111/apifoo", got)
		}
	})

	t.Run("query string preserved", func(t *testing.T) {
		got := mustDecide(t, r, request(t, "GET", "/api/users?x=1&y=2", nil))
		if got != "http://a:1111/api/users?x=1&y=2" {
			t.Errorf("want %q, got %q", "http://a:1111/api/users?x=1&y=2", got)
		}
	})
}

func TestDecideRoute_CatchAllRoot(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{prefixRoute("/", "a")},
	))
	mustDecide(t, r, request(t, "GET", "/anything/here", nil))
	mustDecide(t, r, request(t, "GET", "/", nil))
}

func TestDecideRoute_RewritePrefix(t *testing.T) {
	r := newTestRouter(t, testConfig(
		[]config.Upstream{upstream("a", "http://a:1111")},
		[]config.Route{{Rule: config.Rule{PathPrefix: "/api/test", Rewrite: "/v2"}, Upstream: "a"}},
	))

	cases := []struct{ path, want string }{
		{"/api/test/users", "http://a:1111/v2/users"},
		{"/api/test", "http://a:1111/v2"},
	}
	for _, tc := range cases {
		if got := mustDecide(t, r, request(t, "GET", tc.path, nil)); got != tc.want {
			t.Errorf("GET %s: want %q, got %q", tc.path, tc.want, got)
		}
	}
}

// Config hot reload.
//
// fsnotify attaches to the file inside CreateFromConfig; the swap cannot be
// triggered programmatically, so tests work through temp files. Writes must be
// in-place (os.WriteFile): rename-overwrite kills the watcher attached to the
// file.

const timeoutsYAML = `net-timeouts-s:
  transport: {tcp: 5, keep-alive: 5, tls: 5, response-header: 5, idle-conn: 30}
  server: {context: 10, read-header: 5, idle: 30}
`

const cfgV1YAML = timeoutsYAML + `
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout-ms: 300
routes:
  - rules:
      path-prefix: /v1
    upstream: a
`

const cfgV2YAML = timeoutsYAML + `
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout-ms: 300
  - name: b
    host: http://srv-b:2222
    timeout-ms: 300
routes:
  - rules:
      path-prefix: /v1
    upstream: a
  - rules:
      path-prefix: /v2
    upstream: b
`

// Semantically invalid: route references a non-existent upstream.
const cfgUnknownUpstreamYAML = timeoutsYAML + `
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout-ms: 300
routes:
  - rules:
      path-prefix: /v1
    upstream: a
  - rules:
      path-prefix: /v2
    upstream: ghost
`

const cfgBrokenYAML = `upstreams: [oops`

func startRouterFromTmpConfig(t *testing.T, yamlText string) (configPath string, r *Router) {
	t.Helper()
	configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // stop the watcher if CreateFromConfig attaches one
	r, err := CreateFromConfig(ctx, configPath)
	if err != nil {
		t.Fatalf("CreateFromConfig(%s): %v", configPath, err)
	}
	return configPath, r
}

func rewriteConfigFile(t *testing.T, path, yamlText string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
}

func routesTo(t *testing.T, r *Router, path, wantTarget string) bool {
	t.Helper()
	d, err := r.DecideRoute(request(t, "GET", path, nil))
	return err == nil && d.Target != nil && d.Target.String() == wantTarget
}

func noRouteTo(t *testing.T, r *Router, path string) bool {
	t.Helper()
	d, err := r.DecideRoute(request(t, "GET", path, nil))
	return err != nil && d.Target == nil
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestReload_SwapOnValidConfig(t *testing.T) {
	path, r := startRouterFromTmpConfig(t, cfgV1YAML)

	if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
		t.Fatal("/v1 route not working right after start")
	}
	if !noRouteTo(t, r, "/v2") {
		t.Fatal("/v2 must not match before swap")
	}

	rewriteConfigFile(t, path, cfgV2YAML)
	waitFor(t, 5*time.Second,
		func() bool { return routesTo(t, r, "/v2", "http://srv-b:2222/v2") },
		"valid config swap not picked up by watcher",
	)
	if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
		t.Error("old route /v1 disappeared after swap")
	}
}

func TestCreateFromConfig_MissingConfigFile(t *testing.T) {
	r, err := CreateFromConfig(context.Background(), filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("expected error when starting with a missing config")
	}
	if r != nil {
		t.Errorf("on error router must be nil, got %v", r)
	}
}

// Invalid config at start: CreateFromConfig returns an error (process must not
// start). "broken yaml" already passes via ParseConfig; "unknown upstream" will
// turn green once CreateFromConfig calls config.Validate.
func TestCreateFromConfig_RejectsInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"broken yaml":      cfgBrokenYAML,
		"unknown upstream": cfgUnknownUpstreamYAML,
	}
	for name, yamlText := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
				t.Fatal(err)
			}
			r, err := CreateFromConfig(context.Background(), path)
			if err == nil {
				t.Fatal("expected validation error on start")
			}
			if r != nil {
				t.Errorf("on error router must be nil, got %v", r)
			}
		})
	}
}

func TestReload_InvalidConfigKeepsOld(t *testing.T) {
	cases := map[string]string{
		"broken yaml":      cfgBrokenYAML,
		"unknown upstream": cfgUnknownUpstreamYAML,
	}
	for name, badYAML := range cases {
		t.Run(name, func(t *testing.T) {
			path, r := startRouterFromTmpConfig(t, cfgV1YAML)

			if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
				t.Fatal("initial config not applied")
			}

			rewriteConfigFile(t, path, badYAML)

			deadline := time.Now().Add(1 * time.Second)
			for time.Now().Before(deadline) {
				if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
					t.Fatal("invalid config broke current routing")
				}
				time.Sleep(20 * time.Millisecond)
			}

			rewriteConfigFile(t, path, cfgV2YAML)
			waitFor(t, 5*time.Second,
				func() bool { return routesTo(t, r, "/v2", "http://srv-b:2222/v2") },
				"watcher died after a rejected swap",
			)

			if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
				t.Error("v1 route should survive v2 reload (declared in v2)")
			}
			if !routesTo(t, r, "/v2", "http://srv-b:2222/v2") {
				t.Error("v2 route missing after reload")
			}
		})
	}
}
