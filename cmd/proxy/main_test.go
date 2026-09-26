package main

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// Integration tests for the live stack.
//
// The proxy only exists in main() and is hardcoded to :8080; the backend is a
// separate service, so tests hit the real environment:
//
//	1) proxy:   go run .
//	2) backend: service exposing /api/users-prod, /hang, /hang-body
//	3) add the routes from REQUIRED_ROUTES_YAML to the proxy config
//
// If a piece of the environment is missing, the test SKIPS rather than fails.
// Addresses/paths are overridable via env: PROXY_ADDR, BACKEND_ADDR, API_PATH,
// DEAD_PATH, HANG_PATH, HANG_BODY_PATH.

/* REQUIRED_ROUTES_YAML - add to the proxy config for the hang/dead tests:

upstreams:
  - name: users-prod
    host: http://localhost:3001
    timeout_ms: 2000                  # short on purpose, for timeout tests
  - name: dead
    host: http://localhost:1          # port 1 - guaranteed connection refused
    timeout_ms: 1000

routes:
  - rules: { path_prefix: /api }
    upstream: users-prod
  - rules: { path_prefix: /hang-body }  # longer prefix beats /hang
    upstream: users-prod
  - rules: { path_prefix: /hang }
    upstream: users-prod
  - rules: { path_prefix: /dead }
    upstream: dead
*/

var (
	proxyBase    = getenv("PROXY_ADDR", "http://localhost:8080")
	backendBase  = getenv("BACKEND_ADDR", "http://localhost:3001")
	apiPath      = getenv("API_PATH", "/api/users-prod")
	hangPath     = getenv("HANG_PATH", "/hang")
	hangBodyPath = getenv("HANG_BODY_PATH", "/hang-body")
	// DEAD_PATH is optional: a path routed to an upstream on a dead port.
	// Set - TestProxy_DeadUpstream500 uses it in the shared run.
	// Unset - the test runs in "stop the backend" mode.
	deadPath = os.Getenv("DEAD_PATH")
)

// /hang and /hang-body sleep ~60s; if the proxy replies faster than the budget,
// its timeout fired. We don't assert second-accurate timing by agreement.
const hangBudget = 55 * time.Second

var (
	shortClient = &http.Client{Timeout: 10 * time.Second}
	hangClient  = &http.Client{Timeout: hangBudget + 5*time.Second}
)

func requireProxy(t *testing.T) {
	t.Helper()
	u, err := url.Parse(proxyBase)
	if err != nil {
		t.Fatalf("bad PROXY_ADDR %q: %v", proxyBase, err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond)
	if err != nil {
		t.Skipf("proxy not reachable at %s - start it (go run .): %v", proxyBase, err)
	}
	conn.Close()
}

func getBody(t *testing.T, c *http.Client, target string) (int, string) {
	t.Helper()
	resp, err := c.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", target, err)
	}
	return resp.StatusCode, string(body)
}

// Route found + backend 200 - client sees 200, body unchanged.
func TestProxy_ForwardsUpstreamResponse(t *testing.T) {
	requireProxy(t)

	// Reference directly from the backend; no backend - skip.
	resp, err := shortClient.Get(backendBase + apiPath)
	if err != nil {
		t.Skipf("backend %s not reachable - start it: %v", backendBase, err)
	}
	refBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Skipf("could not read backend body: %v", err)
	}
	refStatus := resp.StatusCode

	gotStatus, gotBody := getBody(t, shortClient, proxyBase+apiPath)

	if gotStatus != refStatus {
		t.Errorf("status via proxy %d, direct from backend %d", gotStatus, refStatus)
	}
	if gotBody != string(refBody) {
		t.Errorf("body corrupted by proxy:\n direct: %q\n proxy:  %q", refBody, gotBody)
	}
}

// No route matched - 404.
func TestProxy_NoRoute404(t *testing.T) {
	requireProxy(t)
	status, _ := getBody(t, shortClient, proxyBase+"/definitely-not-a-route-31337")
	if status != http.StatusNotFound {
		t.Errorf("want 404 for a non-existent route, got %d", status)
	}
}

// Route exists, backend unreachable - 500 (by contract, not 502).
//
// There is no dedicated dead route in the config, so two modes:
//  1. env DEAD_PATH set (route to an upstream on a dead port) - test through it;
//  2. otherwise - stop the backend and run only this test:
//     go test -run TestProxy_DeadUpstream500
//     (live backend - skip, so the shared run stays green: TestProxy_ForwardsUpstreamResponse needs it up).
func TestProxy_DeadUpstream500(t *testing.T) {
	requireProxy(t)

	target := deadPath
	if deadPath == "" {
		if _, err := shortClient.Get(backendBase + apiPath); err == nil {
			t.Skipf("backend %s is up and DEAD_PATH unset - to check the 500, stop the backend and run only this test (or set DEAD_PATH)", backendBase)
		}
		target = apiPath
	}

	status, _ := getBody(t, shortClient, proxyBase+target)
	if status == http.StatusNotFound {
		t.Skipf("route %s not configured on the proxy", target)
	}
	if status != http.StatusInternalServerError {
		t.Errorf("want 500 when the backend is unreachable, got %d", status)
	}
}

// /hang: backend accepted, no response. The proxy must cut it off by its own
// timeout and return 504 instead of hanging for a minute with the backend.
func TestProxy_HangTimesOut504(t *testing.T) {
	requireProxy(t)
	t.Parallel()

	start := time.Now()
	resp, err := hangClient.Get(proxyBase + hangPath)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("request did not finish even within budget %s - proxy did NOT cut it off by timeout: %v", hangBudget, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if elapsed >= hangBudget {
		t.Errorf("proxy held the request %s - longer than the budget", elapsed)
	}
	if resp.StatusCode == http.StatusNotFound {
		t.Skipf("route %s not configured - add it (see REQUIRED_ROUTES_YAML)", hangPath)
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("hang - want 504 (timeout before headers), got %d in %s", resp.StatusCode, elapsed)
	}
}

// /hang-body: backend sent headers (client already saw 200) then hung on the body.
// Status/headers are NOT asserted - they reach the client before the body is
// read. What matters is that the proxy cuts off the hanging BODY within budget.
func TestProxy_HangBodyTimesOut(t *testing.T) {
	requireProxy(t)
	t.Parallel()

	start := time.Now()
	resp, err := hangClient.Get(proxyBase + hangBodyPath)
	if err != nil {
		t.Fatalf("headers from proxy did not arrive within budget %s - timeout not working: %v", hangBudget, err)
	}

	// 404 is used only as a "route not configured" signal, not an assertion.
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		t.Skipf("route %s not configured - add it (see REQUIRED_ROUTES_YAML)", hangBodyPath)
	}

	// Body hangs on the backend - proxy must cut it off by timeout.
	_, rerr := io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start)
	resp.Body.Close()

	if elapsed >= hangBudget {
		t.Errorf("proxy held the request %s (budget %s) - body timeout did not fire", elapsed, hangBudget)
	}
	// Reading the body to EOF is impossible: the backend sends nothing after
	// headers. rerr != nil (unexpected EOF / reset / timeout) - connection cut.
	// If the proxy does NOT cut it off, ReadAll hangs until the client timeout,
	// and elapsed >= budget fails the previous assertion.
	if rerr == nil {
		t.Error("body read to completion although the backend sends nothing - proxy did not cut the connection")
	}
}
