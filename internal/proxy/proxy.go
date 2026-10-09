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
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/routing"
)

type L7Proxy struct {
	mirrorQueue chan ([]byte)
	rp          *httputil.ReverseProxy
	router      *routing.Router
	server      *http.Server
	mux         *http.ServeMux
	metrics     *metrics.RequestMetrics
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
	return p.toSec(p.router.Config().NetworkTimeoutsSec.Server.Context)
}

func (p *L7Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	ctx, cancel := context.WithTimeout(r.Context(), p.contextTimeout())
	defer cancel()

	p.rp.ServeHTTP(wrapped, r.WithContext(ctx))

	if p.metrics != nil {
		p.metrics.Observe(r.Method, r.URL.Path, wrapped.statusCode, time.Since(start))
	}
}

func (p *L7Proxy) setupMux() {
	p.mux = http.NewServeMux()
	p.mux.Handle("/metrics", promhttp.Handler())
	p.mux.Handle("/", p)
}

func (p *L7Proxy) rewrite(pr *httputil.ProxyRequest) {
	route, err := p.router.DecideRoute(pr.In)

	if err != nil {
		ctx := context.WithValue(pr.Out.Context(), "proxyError", http.StatusNotFound)
		pr.Out = pr.Out.WithContext(ctx)
		// FIXME: ErrorHandler is triggered because scheme is ""
		// because the route wasn't found, not because we entered this block
		// it works but it DOESN'T LET ME SLEEP
		return
	}

	if pr.In.Body != nil && pr.In.Body != http.NoBody && pr.In.ContentLength != 0 {
		body, err := io.ReadAll(pr.In.Body)
		pr.Out.Body = io.NopCloser(bytes.NewReader(body))

		if err != nil {
			slog.Warn("Unable to read request body", "body_len", len(body), "error", err)
		} else {
			// Copy not required until a sync.Pool is introduced
			// bufCopy := make([]byte, len(body))
			// copy(bufCopy, body)

			select {
			case p.mirrorQueue <- body:
			default:
				slog.Warn("Buffer overflow")
			}
		}
	}

	slog.Debug("Router decision", "route", route)

	pr.SetXForwarded()
	pr.Out.URL = route
}

func (p *L7Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	slog.Info("Proxy HTTP error", "method", r.Method, "path", r.URL.Path, "error", err)

	e := r.Context().Value("proxyError")

	switch {
	case e == http.StatusBadRequest:
		http.Error(w, "Bad request: ", http.StatusBadRequest)
	case e == http.StatusNotFound:
		http.NotFound(w, r)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "Request timed out on the server", http.StatusGatewayTimeout)
	default:
		http.Error(w, "Bad Gateway: unexpected error", http.StatusBadGateway)
	}
}

func (p *L7Proxy) modifyResponse(resp *http.Response) error {
	method := resp.Request.Method
	status := resp.StatusCode
	path := resp.Request.URL.Path
	slog.Debug("Backend response", "method", method, "status_code", status, "path", path)
	return nil
}

func (p *L7Proxy) buildTransport() *http.Transport {
	toSec := func(d int) time.Duration { return time.Duration(d) * time.Second }

	timeoutsTransport := p.router.Config().NetworkTimeoutsSec.Transport
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   toSec(timeoutsTransport.TCP),
			KeepAlive: toSec(timeoutsTransport.KeepAlive),
		}).DialContext,
		TLSHandshakeTimeout:   toSec(timeoutsTransport.TLS),
		ResponseHeaderTimeout: toSec(timeoutsTransport.ResponseHeader),
		IdleConnTimeout:       toSec(timeoutsTransport.IdleConn),
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   1000,
		DisableKeepAlives:     false,
		MaxConnsPerHost:       0,
		ForceAttemptHTTP2:     true,
	}
}

func New(r *routing.Router, port string, m *metrics.RequestMetrics) *L7Proxy {
	slog.Debug("CONFIG", "config", r.Config())

	proxy := &L7Proxy{
		router:      r,
		mirrorQueue: make(chan []byte, 100),
		metrics:     m,
	}

	proxy.rp = &httputil.ReverseProxy{
		Rewrite:        proxy.rewrite,
		Transport:      proxy.buildTransport(),
		ErrorHandler:   proxy.errorHandler,
		ModifyResponse: proxy.modifyResponse,
	}

	proxy.setupMux()

	timeoutsServer := proxy.router.Config().NetworkTimeoutsSec.Server

	proxy.server = &http.Server{
		Addr:              ":" + port,
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
		done := make(chan struct{})
		go func() { p.router.Wait(); close(done) }()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("waiting for goroutines timed out")
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := p.server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Shutdown failed", "error", err)
			return fmt.Errorf("shutdown: %w", err)
		}

		slog.Info("Server stopped")

		return nil

	case err := <-errCh:
		return err
	}
}
