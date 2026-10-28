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
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/mirroring"
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
}

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

func New(r *routing.Router, infraConfig *config.InfrastructureConfig, m *metrics.RequestMetrics, mirroringPool *mirroring.Pool) *L7Proxy {
	slog.Debug("INFRASTRUCTURE CONFIG", "config", infraConfig)

	proxy := &L7Proxy{
		router:        r,
		metrics:       m,
		mirroringPool: mirroringPool,
		infraConfig:   infraConfig,
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
