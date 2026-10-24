package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/unisom0rphic/l7proxy/internal/config"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
	"github.com/unisom0rphic/l7proxy/internal/mirroring"
	"github.com/unisom0rphic/l7proxy/internal/proxy"
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
		Filename:   "/tmp/proxy.log",
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}

	// pprof
	pprofServer := &http.Server{
		Addr:              "127.0.0.1:6060",
		Handler:           nil,
		ReadHeaderTimeout: 3 * time.Second,
	}

	go func() {
		slog.Info("Start pprof", "listen_and_serve", pprofServer.ListenAndServe())
	}()

	slogOpts := &slog.HandlerOptions{
		AddSource: false,
		Level:     slog.LevelDebug,
	}
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	logHandler := slog.NewTextHandler(multiWriter, slogOpts)
	slog.SetDefault(slog.New(logHandler))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Parse infrastructure config
	infraConfigPath := getenv("INFRA_CONFIG_PATH", "infrastructure.yaml")
	infraConfig, err := config.ParseInfrastructureConfig(infraConfigPath)
	if err != nil {
		slog.Error("Unable to parse infrastructure config", "error", err)
		panic("Incorrect infrastructure configuration, unable to start the server")
	}

	errs := infraConfig.Validate()
	if len(errs) > 0 {
		for _, e := range errs {
			slog.Error("infrastructure config validation error", "err", e)
		}
		panic("Invalid infrastructure configuration")
	}

	// Parse routing config
	routesConfigPath := getenv("ROUTES_CONFIG_PATH", "routes.yaml")
	proxyRouter, err := routing.CreateFromConfig(ctx, routesConfigPath)
	if err != nil {
		slog.Error("Unable to create router", "error", err)
		panic("Incorrect routing configuration, unable to start the server")
	}

	// Create mirror pool with config values
	proxyMirror := mirroring.NewPool(
		infraConfig.Mirroring.Workers,
		infraConfig.Mirroring.QueueSize,
		metrics.Mirror,
	)

	prometheus.MustRegister(metrics.HTTPRequestsTotal)
	prometheus.MustRegister(metrics.HTTPRequestDuration)
	prometheus.MustRegister(metrics.ConfigReloadErrorsTotal)
	prometheus.MustRegister(metrics.MirrorRequestsTotal)

	// Port from infrastructure config or env var override
	port := getenv("PORT", infraConfig.Port)
	if port != "" {
		infraConfig.Port = port
	}
	p := proxy.New(proxyRouter, infraConfig, metrics.Default, proxyMirror)
	if err = p.Run(ctx); err != nil {
		slog.Error("Proxy error", "error", err)
	}
}
