package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/mirroring"
	"github.com/unisom0rphic/l7proxy/internal/ratelimit"
	"github.com/unisom0rphic/l7proxy/internal/routing"
)

type L7Proxy struct {
	mirroringPool *mirroring.Pool
	rp            *httputil.ReverseProxy
	router        *routing.Router
	server        *http.Server
	mux           *http.ServeMux
	metrics       *metrics.RequestMetrics
	infraConfig   *config.InfrastructureConfig
	gate          Gate
	limiter       Limiter
}

// Gate is the seam for the future circuit breaker. Allow reports whether an
// upstream may serve a request; Record feeds the outcome of an attempt back so
// the gate can trip after repeated failures. A real implementation belongs in
// internal/policy; until then the default admits everything.
type Gate interface {
	Allow(upstream string) bool
	Record(upstream string, err error)
}

// Limiter reports whether an upstream has capacity for another request.
// The token-bucket implementation lives in internal/ratelimit; the default
// admits everything.
type Limiter interface {
	Allow(upstream string) bool
}

// allowAllGate is the default Gate: every upstream is admitted and outcomes
// are discarded, so a deployment without a policy layer behaves as if no
// breaker exists.
type allowAllGate struct{}

func (allowAllGate) Allow(string) bool    { return true }
func (allowAllGate) Record(string, error) {}

// unlimitedLimiter is the default Limiter: every upstream always has capacity.
type unlimitedLimiter struct{}

func (unlimitedLimiter) Allow(string) bool { return true }

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijacking not supported")
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.statusCode == 0 {
		rw.statusCode = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}

	return rw.ResponseWriter.Write(b)
}

func (p *L7Proxy) toSec(d int) time.Duration { return time.Duration(d) * time.Second }

func (p *L7Proxy) contextTimeout() time.Duration {
	return p.toSec(p.infraConfig.NetworkTimeoutsSec.Server.Context)
}

type decisionKey struct{}

const upstreamUnmatched = "unmatched"

func (p *L7Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if maxSize := p.infraConfig.MaxBodySize; maxSize > 0 && r.ContentLength > maxSize {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		p.observe(r.Method, upstreamUnmatched, http.StatusRequestEntityTooLarge, start)
		return
	}

	decision, err := p.router.DecideRoute(r)
	if err != nil {
		slog.Debug("no route for request", "method", r.Method, "path", r.URL.Path, "error", err)
		http.NotFound(w, r)
		p.observe(r.Method, upstreamUnmatched, http.StatusNotFound, start)
		return
	}

	// Multipart/streaming -> Content-Length might be -1, thus use MaxBytesReader
	if maxSize := p.infraConfig.MaxBodySize; maxSize > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxSize)
	}

	wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	ctx, cancel := context.WithTimeout(r.Context(), p.contextTimeout())
	defer cancel()
	ctx = context.WithValue(ctx, decisionKey{}, decision)

	p.rp.ServeHTTP(wrapped, r.WithContext(ctx))

	p.observe(r.Method, decision.Upstream, wrapped.statusCode, start)
}

func (p *L7Proxy) observe(method, upstream string, status int, start time.Time) {
	if p.metrics != nil {
		p.metrics.Observe(method, upstream, status, time.Since(start))
	}
}

func (p *L7Proxy) setupMux() {
	p.mux = http.NewServeMux()
	p.mux.Handle("/metrics", promhttp.Handler())
	p.mux.Handle("/", p)
}

func (p *L7Proxy) rewrite(pr *httputil.ProxyRequest) {
	decision, ok := pr.In.Context().Value(decisionKey{}).(routing.Decision)
	if !ok {
		// Unreachable while ServeHTTP decides before handing over to the proxy.
		slog.Error("rewrite called without a routing decision", "path", pr.In.URL.Path)
		return
	}

	// Currently works according to ADR-0003
	// TODO: reimplement because pr.Out.Body is incomplete
	// if ReadAll failed
	body, err := io.ReadAll(pr.In.Body)
	pr.Out.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		slog.Warn("unable to read request body, not mirroring", "error", err)
	} else {
		// The body is buffered, so a failed attempt can be replayed against
		// the fallback. An absent GetBody marks a body that was not fully
		// read, which failover refuses.
		pr.Out.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	pr.SetXForwarded()

	pr.Out.URL = decision.Target
	pr.Out.Host = ""

	if err == nil && len(decision.Mirrors) > 0 {
		p.mirroringPool.TrySubmit(mirroring.NewJob(
			pr.In.Method,
			body,
			pr.Out.Header.Clone(),
			decision.Target.Path,
			decision.Target.RawQuery,
			decision.Mirrors,
		))
	}
}

func (p *L7Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	slog.Info("Proxy HTTP error", "method", r.Method, "path", r.URL.Path, "error", err)

	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, errRateLimited):
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	case errors.Is(err, errUpstreamUnavailable):
		http.Error(w, "Upstream unavailable", http.StatusServiceUnavailable)
		return
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "Request timed out on the server", http.StatusGatewayTimeout)
		return
	case errors.Is(err, context.Canceled):
		return
	case errors.As(err, &maxBytes):
		http.Error(w, "Request body too big", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "Bad Gateway: unexpected error", http.StatusBadGateway)
}

func (p *L7Proxy) modifyResponse(resp *http.Response) error {
	method := resp.Request.Method
	status := resp.StatusCode
	path := resp.Request.URL.Path
	slog.Debug("Backend response", "method", method, "status_code", status, "path", path)
	return nil
}

func (p *L7Proxy) buildTransport(infraConfig *config.InfrastructureConfig) *http.Transport {
	toSec := func(d int) time.Duration { return time.Duration(d) * time.Second }

	timeoutsTransport := infraConfig.NetworkTimeoutsSec.Transport
	connPool := infraConfig.ConnectionPool
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   toSec(timeoutsTransport.TCP),
			KeepAlive: toSec(timeoutsTransport.KeepAlive),
		}).DialContext,
		TLSHandshakeTimeout:   toSec(timeoutsTransport.TLS),
		ResponseHeaderTimeout: toSec(timeoutsTransport.ResponseHeader),
		IdleConnTimeout:       toSec(timeoutsTransport.IdleConn),
		MaxIdleConns:          connPool.MaxIdleConns,
		MaxIdleConnsPerHost:   connPool.MaxIdleConnsPerHost,
		DisableKeepAlives:     false,
		MaxConnsPerHost:       0,
		ForceAttemptHTTP2:     true,
	}
}

var errUpstreamUnavailable = errors.New("upstream unavailable")

var errRateLimited = errors.New("rate limited")

// attemptingTransport bounds every attempt with the routed upstream's timeout
// and fails over to the configured fallback when the primary cannot be
// reached. It wraps buildTransport, so all proxied requests go through it.
type attemptingTransport struct {
	base http.RoundTripper
	p    *L7Proxy
}

func (t *attemptingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	decision, ok := req.Context().Value(decisionKey{}).(routing.Decision)
	if !ok {
		// Unreachable while ServeHTTP decides before handing over to the proxy.
		return t.base.RoundTrip(req)
	}

	resp, err := t.attempt(req, decision.Upstream, decision.Target, decision.Timeout)
	if err == nil {
		return resp, nil
	}

	// The overall ceiling may already be spent, or the request may not be
	// safe to replay: in either case retrying would only waste the budget.
	if req.Context().Err() != nil || !canFailover(req, decision) {
		return nil, err
	}

	slog.Warn("upstream attempt failed, falling back",
		"upstream", decision.Upstream,
		"fallback", decision.Fallback.Name,
		"method", req.Method,
		"error", err)

	resp, ferr := t.attempt(req, decision.Fallback.Name, decision.Fallback.URL, decision.Fallback.Timeout)
	if ferr != nil {
		return nil, ferr
	}
	return resp, nil
}

// canFailover reports whether a failed request may be replayed against the
// fallback: one is configured, the method is idempotent, and the body was
// buffered so it can be sent again.
func canFailover(req *http.Request, d routing.Decision) bool {
	return d.Fallback != nil && idempotent(req.Method) && req.GetBody != nil
}

// attempt runs one upstream attempt, consulting the limiter and the gate
// first. The outcome is recorded so a future breaker can trip on it. No second
// failover happens here: a fallback is followed for one hop only.
func (t *attemptingTransport) attempt(req *http.Request, name string, target *url.URL, timeout time.Duration) (*http.Response, error) {
	if !t.p.limiter.Allow(name) {
		return nil, fmt.Errorf("%w: upstream %s", errRateLimited, name)
	}
	if !t.p.gate.Allow(name) {
		return nil, fmt.Errorf("%w: upstream %s", errUpstreamUnavailable, name)
	}

	resp, err := t.send(req, target, timeout)
	t.p.gate.Record(name, err)
	return resp, err
}

// send issues a single HTTP attempt against target under its own deadline. The
// deadline is released once the response body is closed.
func (t *attemptingTransport) send(req *http.Request, target *url.URL, timeout time.Duration) (*http.Response, error) {
	ctx := req.Context()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}

	out := req.Clone(ctx)
	u := *req.URL
	u.Scheme = target.Scheme
	u.Host = target.Host
	out.URL = &u
	out.Host = ""

	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return nil, err
		}
		out.Body = body
	}

	resp, err := t.base.RoundTrip(out)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	if cancel != nil {
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

// cancelOnClose releases an attempt's deadline when its body is done, so the
// timer does not linger for the rest of the timeout.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// idempotent reports whether a failed request may be replayed on another
// upstream. POST and PATCH are excluded because repeating them may duplicate
// work.
func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func New(r *routing.Router, infraConfig *config.InfrastructureConfig, m *metrics.RequestMetrics, mirroringPool *mirroring.Pool) *L7Proxy {
	slog.Debug("INFRASTRUCTURE CONFIG", "config", infraConfig)

	var limiter Limiter = unlimitedLimiter{}
	if infraConfig.RateLimit.Enabled {
		rl, err := ratelimit.New(infraConfig.RateLimit)
		if err != nil {
			panic(fmt.Sprintf("rate limiter: %v", err))
		}
		limiter = rl
	}

	proxy := &L7Proxy{
		router:        r,
		metrics:       m,
		mirroringPool: mirroringPool,
		infraConfig:   infraConfig,
		gate:          allowAllGate{},
		limiter:       limiter,
	}

	proxy.rp = &httputil.ReverseProxy{
		Rewrite:        proxy.rewrite,
		Transport:      &attemptingTransport{base: proxy.buildTransport(infraConfig), p: proxy},
		ErrorHandler:   proxy.errorHandler,
		ModifyResponse: proxy.modifyResponse,
	}

	proxy.setupMux()

	timeoutsServer := infraConfig.NetworkTimeoutsSec.Server

	proxy.server = &http.Server{
		Addr:              ":" + infraConfig.Port,
		Handler:           proxy.mux,
		ReadHeaderTimeout: proxy.toSec(timeoutsServer.ReadHeader),
		IdleTimeout:       proxy.toSec(timeoutsServer.Idle),
	}

	return proxy
}

func (p *L7Proxy) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		err := p.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		slog.Info("Shutdown signal received")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := p.server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Shutdown failed", "error", err)
			//nolint:errcheck,gosec
			p.server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}

		done := make(chan struct{})
		go func() { p.router.Wait(); p.mirroringPool.Stop(); close(done) }()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("waiting for mirror module shutdown timed out")
		}

		slog.Info("Server stopped")

		return nil

	case err := <-errCh:
		return err
	}
}
