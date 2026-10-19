package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/mirroring"
	"github.com/unisom0rphic/l7proxy/internal/routing"
)

type L7Proxy struct {
	mirrorModule *mirroring.MirrorModule
	rp           *httputil.ReverseProxy
	router       *routing.Router
	server       *http.Server
	mux          *http.ServeMux
	metrics      *metrics.RequestMetrics
	infraConfig  *config.InfrastructureConfig
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
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

// decisionKey carries the routing decision from ServeHTTP into rewrite, so the
// route is resolved once per request.
type decisionKey struct{}

func (p *L7Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	decision, err := p.router.DecideRoute(r)
	if err != nil {
		slog.Debug("no route for request", "method", r.Method, "path", r.URL.Path, "error", err)
		http.NotFound(w, r)
		p.observe(r, http.StatusNotFound, start)
		return
	}

	wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	ctx, cancel := context.WithTimeout(r.Context(), p.contextTimeout())
	defer cancel()
	ctx = context.WithValue(ctx, decisionKey{}, decision)

	p.rp.ServeHTTP(wrapped, r.WithContext(ctx))

	p.observe(r, wrapped.statusCode, start)
}

func (p *L7Proxy) observe(r *http.Request, status int, start time.Time) {
	if p.metrics != nil {
		p.metrics.Observe(r.Method, r.URL.Path, status, time.Since(start))
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

	body, err := io.ReadAll(pr.In.Body)
	pr.Out.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		slog.Warn("unable to read request body, not mirroring", "error", err)
	}

	pr.SetXForwarded()

	// Not SetURL: it joins the target path onto the inbound one, and the target
	// already carries the full outbound path. Clearing Host makes the upstream
	// receive its own host instead of the client's.
	pr.Out.URL = decision.Target
	pr.Out.Host = ""

	if err == nil && len(decision.Mirrors) > 0 {
		p.mirrorModule.TrySubmit(mirroring.NewJob(
			pr.In.Method,
			body,
			pr.Out.Header,
			decision.Target.Path,
			decision.Target.RawQuery,
			decision.Mirrors,
		))
	}
}

func (p *L7Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	slog.Info("Proxy HTTP error", "method", r.Method, "path", r.URL.Path, "error", err)

	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "Request timed out on the server", http.StatusGatewayTimeout)
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

func New(r *routing.Router, infraConfig *config.InfrastructureConfig, m *metrics.RequestMetrics, mm *mirroring.MirrorModule) *L7Proxy {
	slog.Debug("INFRASTRUCTURE CONFIG", "config", infraConfig)

	proxy := &L7Proxy{
		router:       r,
		metrics:      m,
		mirrorModule: mm,
		infraConfig:  infraConfig,
	}

	proxy.rp = &httputil.ReverseProxy{
		Rewrite:        proxy.rewrite,
		Transport:      proxy.buildTransport(infraConfig),
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
			return fmt.Errorf("shutdown: %w", err)
		}

		done := make(chan struct{})
		go func() { p.router.Wait(); p.mirrorModule.Stop(); close(done) }()

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
