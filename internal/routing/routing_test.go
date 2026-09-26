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
	return &config.Config{Upstreams: upstreams, Routes: routes}
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
	u, err := r.DecideRoute(req)
	if err != nil {
		t.Fatalf("DecideRoute(%s %s): unexpected error: %v", req.Method, req.URL.Path, err)
	}
	if u == nil {
		t.Fatalf("DecideRoute(%s %s): nil URL without error (contract violation)", req.Method, req.URL.Path)
	}
	return u.String()
}

func noDecide(t *testing.T, r *Router, req *http.Request) {
	t.Helper()
	u, err := r.DecideRoute(req)
	if err == nil {
		t.Fatalf("DecideRoute(%s %s): expected miss, got %q", req.Method, req.URL.Path, u)
	}
	if u != nil {
		t.Fatalf("DecideRoute(%s %s): error present but URL is not nil: %q", req.Method, req.URL.Path, u)
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
		[]config.Upstream{upstream("a", "http://a:1111"), upstream("b", "http://b:2222")},
		[]config.Route{prefixRoute("/api", "a"), prefixRoute("/api/v2", "b")},
	))

	cases := []struct{ path, want string }{
		{"/api/v2/users", "http://b:2222/api/v2/users"},
		{"/api/v2", "http://b:2222/api/v2"},
		{"/api/other", "http://a:1111/api/other"},
		{"/api", "http://a:1111/api"},
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
	u, err := r.DecideRoute(&http.Request{})
	if err == nil {
		t.Fatalf("expected error for request without URL, got %q", u)
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
		[]config.Route{{Rule: config.Rule{PathPrefix: "/api", Header: "X-Api-Version"}, Upstream: "a"}},
	))

	h := http.Header{}
	h.Set("X-Api-Version", "whatever-value")
	mustDecide(t, r, request(t, "GET", "/api/users", h))
	noDecide(t, r, request(t, "GET", "/api/users", nil))
}

func TestDecideRoute_HeaderExactValue(t *testing.T) {
	t.Skip("TODO: exact header value match not implemented — only presence is checked")
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
		[]config.Route{{Rule: config.Rule{PathPrefix: "/api", Method: "POST", Header: "X-Debug"}, Upstream: "a"}},
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

const timeoutsYAML = `net_timeouts_s:
  transport: {tcp: 5, keep_alive: 5, tls: 5, response_header: 5, idle_conn: 30}
  server: {context: 10, read_header: 5, idle: 30}
`

const cfgV1YAML = timeoutsYAML + `
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout_ms: 300
routes:
  - rules:
      path_prefix: /v1
    upstream: a
`

const cfgV2YAML = timeoutsYAML + `
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout_ms: 300
  - name: b
    host: http://srv-b:2222
    timeout_ms: 300
routes:
  - rules:
      path_prefix: /v1
    upstream: a
  - rules:
      path_prefix: /v2
    upstream: b
`

// Semantically invalid: route references a non-existent upstream.
const cfgUnknownUpstreamYAML = timeoutsYAML + `
upstreams:
  - name: a
    host: http://srv-a:1111
    timeout_ms: 300
routes:
  - rules:
      path_prefix: /v1
    upstream: a
  - rules:
      path_prefix: /v2
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
	u, err := r.DecideRoute(request(t, "GET", path, nil))
	return err == nil && u != nil && u.String() == wantTarget
}

func noRouteTo(t *testing.T, r *Router, path string) bool {
	t.Helper()
	u, err := r.DecideRoute(request(t, "GET", path, nil))
	return err != nil && u == nil
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

func TestReload_InvalidConfigKeepsOld(t *testing.T) {
	cases := map[string]string{
		"broken yaml":      cfgBrokenYAML,
		"unknown upstream": cfgUnknownUpstreamYAML,
	}
	for name, badYAML := range cases {
		t.Run(name, func(t *testing.T) {
			path, r := startRouterFromTmpConfig(t, cfgV1YAML)

			rewriteConfigFile(t, path, badYAML)
			time.Sleep(500 * time.Millisecond) // give the watcher a chance to fail; it must not
			if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
				t.Error("invalid config broke current routing")
			}

			rewriteConfigFile(t, path, cfgV2YAML)
			waitFor(t, 5*time.Second,
				func() bool { return routesTo(t, r, "/v2", "http://srv-b:2222/v2") },
				"watcher died after a rejected swap",
			)
		})
	}
}

func TestReload_DeletedConfigKeepsOld(t *testing.T) {
	path, r := startRouterFromTmpConfig(t, cfgV1YAML)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// Several checks with pauses: routing must not break.
	for i := 0; i < 5; i++ {
		if !routesTo(t, r, "/v1", "http://srv-a:1111/v1") {
			t.Fatal("routing broke after config file deletion")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Note: file restoration may NOT be picked up (the watcher attached to the
	// file can die on deletion); no contract is asserted for that.
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
