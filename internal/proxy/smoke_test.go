package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/mirroring"
	"github.com/unisom0rphic/l7proxy/internal/proxy"
	"github.com/unisom0rphic/l7proxy/internal/routing"
)

// Self-contained end-to-end test.
// Does not require anything running.

const smokeRoutesYAML = `upstreams:
  - name: users-prod
    host: %s
    timeout-ms: 500
  - name: users-test
    host: %s
    timeout-ms: 500
  - name: orders
    host: %s
    timeout-ms: 500

routes:
  - rules:
      path-prefix: /api/users
    upstream: users-prod
    mirrors:
      - upstream: users-test
        ratio: 1

  - rules:
      path-prefix: /orders
      rewrite: /api/orders
    upstream: orders
`

const smokeInfraYAML = `net-timeouts-s:
  transport:
    tcp: 5
    keep-alive: 30
    tls: 5
    response-header: 5
    idle-conn: 30
  server:
    context: 5
    read-header: 5
    idle: 30

connection-pool:
  max-idle-conns: 32
  max-idle-conns-per-host: 8

mirroring:
  workers: 1
  queue-size: 10

port: "%d"
max-body-size: 4096
`

type backendRequest struct {
	method string
	path   string
	query  string
	body   string
	host   string
}

func TestSmoke_EndToEnd(t *testing.T) {
	prodSeen := make(chan backendRequest, 4)
	mirrorSeen := make(chan backendRequest, 4)
	ordersSeen := make(chan backendRequest, 4)

	prod := newSmokeBackend(t, "users-prod", prodSeen)
	mirrorBackend := newSmokeBackend(t, "users-test", mirrorSeen)
	orders := newSmokeBackend(t, "orders", ordersSeen)

	dir := t.TempDir()
	routesPath := filepath.Join(dir, "routes.yaml")
	infraPath := filepath.Join(dir, "infrastructure.yaml")
	port := freePort(t)

	writeFile(t, routesPath, fmt.Sprintf(smokeRoutesYAML, prod.URL, mirrorBackend.URL, orders.URL))
	writeFile(t, infraPath, fmt.Sprintf(smokeInfraYAML, port))

	infraConfig, err := config.ParseInfrastructureConfig(infraPath)
	if err != nil {
		t.Fatalf("parse infrastructure config: %v", err)
	}
	if errs := infraConfig.Validate(); len(errs) > 0 {
		t.Fatalf("invalid infrastructure config: %v", errs)
	}

	registerMetrics(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	router, err := routing.CreateFromConfig(ctx, routesPath)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	p := proxy.New(router, infraConfig, metrics.Default, mirroring.NewPool(
		infraConfig.Mirroring.Workers,
		infraConfig.Mirroring.QueueSize,
		metrics.Mirror,
	))

	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("proxy.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("proxy did not shut down within 10s")
		}
	})

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitReady(t, base)

	// Routing: prefix match, body forwarded, Host replaced by the upstream's.
	if code, body := smokeDo(t, http.MethodPost, base+"/api/users/42", "hello"); code != http.StatusOK || body != "users-prod:/api/users/42" {
		t.Fatalf("POST /api/users/42: status %d, body %q", code, body)
	}
	prodReq := nextRequest(t, prodSeen, "users-prod")
	if prodReq.method != http.MethodPost || prodReq.path != "/api/users/42" || prodReq.body != "hello" {
		t.Errorf("users-prod received %+v", prodReq)
	}
	if want := hostOf(t, prod.URL); prodReq.host != want {
		t.Errorf("users-prod host: want %q, got %q", want, prodReq.host)
	}

	// Mirroring: the copy carries the same method, path, query and body.
	mirrored := nextRequest(t, mirrorSeen, "users-test")
	if mirrored.method != http.MethodPost || mirrored.path != "/api/users/42" || mirrored.body != "hello" {
		t.Errorf("mirror received %+v", mirrored)
	}

	// Rewrite: the prefix is replaced and the query survives.
	if code, body := smokeDo(t, http.MethodGet, base+"/orders/7?q=1", ""); code != http.StatusOK || body != "orders:/api/orders/7" {
		t.Fatalf("GET /orders/7?q=1: status %d, body %q", code, body)
	}
	ordersReq := nextRequest(t, ordersSeen, "orders")
	if ordersReq.path != "/api/orders/7" || ordersReq.query != "q=1" {
		t.Errorf("orders received %+v", ordersReq)
	}

	if code, _ := smokeDo(t, http.MethodGet, base+"/nope", ""); code != http.StatusNotFound {
		t.Errorf("GET /nope: want 404, got %d", code)
	}

	// Metrics: the upstream name is the label, the path is not.
	if n := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues(http.MethodPost, "users-prod", "200")); n != 1 {
		t.Errorf("http_requests_total{upstream=users-prod}: want 1, got %v", n)
	}
	if n := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues(http.MethodGet, "orders", "200")); n != 1 {
		t.Errorf("http_requests_total{upstream=orders}: want 1, got %v", n)
	}
	if n := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues(http.MethodGet, "unmatched", "404")); n < 1 {
		t.Errorf("http_requests_total{upstream=unmatched}: want at least 1, got %v", n)
	}
	waitForCounter(t, metrics.MirrorRequestsTotal.WithLabelValues(metrics.MirrorSent), 1)

	// The scrape endpoint serves those counters.
	if _, body := smokeDo(t, http.MethodGet, base+"/metrics", ""); !strings.Contains(body, `upstream="users-prod"`) ||
		!strings.Contains(body, `mirror_requests_total{result="sent"}`) {
		t.Errorf("/metrics is missing the expected series:\n%s", body)
	}
}

// newSmokeBackend reports every request it receives on seen and answers
// "<name>:<path>".
func newSmokeBackend(t *testing.T, name string, seen chan<- backendRequest) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case seen <- backendRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(body),
			host:   r.Host,
		}:
		default:
		}
		fmt.Fprintf(w, "%s:%s", name, r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func smokeDo(t *testing.T, method, target, body string) (int, string) {
	t.Helper()

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, target, rdr)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, target, err)
	}

	return resp.StatusCode, string(respBody)
}

func nextRequest(t *testing.T, seen <-chan backendRequest, name string) backendRequest {
	t.Helper()

	select {
	case r := <-seen:
		return r
	case <-time.After(5 * time.Second):
		t.Fatalf("%s received no request", name)
		return backendRequest{}
	}
}

func waitReady(t *testing.T, base string) {
	t.Helper()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)

	for {
		resp, err := client.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy never started listening on %s: %v", base, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForCounter(t *testing.T, c prometheus.Counter, want float64) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := testutil.ToFloat64(c); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("counter is %v, want at least %v", testutil.ToFloat64(c), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", rawURL, err)
	}
	return u.Host
}

// registerMetrics mirrors what main does, so /metrics exposes the counters.
func registerMetrics(t *testing.T) {
	t.Helper()

	collectors := []prometheus.Collector{
		metrics.HTTPRequestsTotal,
		metrics.HTTPRequestDuration,
		metrics.ConfigReloadErrorsTotal,
		metrics.MirrorRequestsTotal,
	}
	for _, c := range collectors {
		if err := prometheus.Register(c); err != nil {
			var alreadyErr prometheus.AlreadyRegisteredError
			if errors.As(err, &alreadyErr) {
				t.Fatalf("register %T: %v", c, err)
			}
		}
	}
}
