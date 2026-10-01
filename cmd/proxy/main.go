package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unisom0rphic/l7proxy/internal/routing"
	"gopkg.in/natefinch/lumberjack.v2"
)

func getenv(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

func main() {
	// Logging
	logFile := &lumberjack.Logger{
		Filename:   "proxy.log",
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}

	multiWriter := io.MultiWriter(logFile, os.Stdout)
	slogOpts := &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	}
	logHandler := slog.NewTextHandler(multiWriter, slogOpts)
	slog.SetDefault(slog.New(logHandler))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	configPath := getenv("CONFIG_PATH", "config.yaml")
	proxyRouter, err := routing.CreateFromConfig(ctx, configPath)

	if err != nil {
		slog.Error("Unable to create router", "error", err)
		panic("Incorrect router configuration, unable to start the server")
	}

	slog.Info("CONFIG", "config", proxyRouter.Config())

	toSec := func(d int) time.Duration { return time.Duration(d) * time.Second }

	// TODO: make fields unexported and provide only getters
	timeoutsTransport := proxyRouter.Config().NetworkTimeoutsSec.Transport
	timeoutsServer := proxyRouter.Config().NetworkTimeoutsSec.Server

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			route, err := proxyRouter.DecideRoute(pr.In)

			if err != nil {
				ctx := context.WithValue(pr.Out.Context(), "proxyError", http.StatusNotFound)
				pr.Out = pr.Out.WithContext(ctx)
				// FIXME: ErrorHandler is triggered because scheme is ""
				// because the route wasn't found, not because we entered this block
				// it works but it DOESN'T LET ME SLEEP
				return
			}

			slog.Debug("Router decision", "route", route)

			pr.SetXForwarded()
			pr.Out.URL = route
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Info("Proxy HTTP error", "method", r.Method, "path", r.URL.Path, "error", err)
			e := r.Context().Value("proxyError")

			if e == http.StatusBadRequest {
				http.Error(w, "Bad request: ", http.StatusBadRequest)
				return
			} else if e == http.StatusNotFound {
				http.NotFound(w, r)
				return
			} else if errors.Is(err, context.DeadlineExceeded) {
				http.Error(w, "Request timed out on the server", http.StatusGatewayTimeout)
				return
			} else {
				http.Error(w, "Bad Gateway: unexpected error", http.StatusBadGateway)
				return
			}
		},

		ModifyResponse: func(resp *http.Response) error {
			method := resp.Request.Method
			status := resp.StatusCode
			path := resp.Request.URL.Path
			slog.Debug("Backend response", "method", method, "status_code", status, "path", path)
			return nil
		},

		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   toSec(timeoutsTransport.TCP),
				KeepAlive: toSec(timeoutsTransport.KeepAlive),
			}).DialContext,
			TLSHandshakeTimeout:   toSec(timeoutsTransport.TLS),
			ResponseHeaderTimeout: toSec(timeoutsTransport.ResponseHeader),
			IdleConnTimeout:       toSec(timeoutsTransport.IdleConn),
		},
	}

	port := getenv("PORT", "8080")

	server := &http.Server{
		Addr: ":" + port,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// FIXME: context timeout depends on server timeout
			// (user can define how long to wait for the response body)
			// SOLUTION: basically just map name to timeout on config init
			ctx, cancel := context.WithTimeout(r.Context(), toSec(timeoutsServer.Context))
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(ctx))
		}),
		ReadHeaderTimeout: toSec(timeoutsServer.ReadHeader),
		IdleTimeout:       toSec(timeoutsServer.Idle),
	}

	go func() {
		slog.Info("Proxy running", "port", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("Shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Shutdown failed", "error", err)
	}

	// clean up will be here
	slog.Info("Server stopped")

}
