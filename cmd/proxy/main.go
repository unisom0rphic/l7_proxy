package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	_ "net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/unisom0rphic/l7proxy/internal/metrics"
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
	go func() {
		slog.Info("Start pprof", "listen_and_serve", http.ListenAndServe("localhost:6060", nil))
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

	configPath := getenv("CONFIG_PATH", "config.yaml")
	proxyRouter, err := routing.CreateFromConfig(ctx, configPath)

	if err != nil {
		slog.Error("Unable to create router", "error", err)
		panic("Incorrect router configuration, unable to start the server")
	}

	prometheus.MustRegister(metrics.HTTPRequestsTotal)
	prometheus.MustRegister(metrics.HTTPRequestDuration)
	prometheus.MustRegister(metrics.ConfigReloadErrorsTotal)

	port := getenv("PORT", "8080")
	p := proxy.New(proxyRouter, port, metrics.Default)
	p.Run(ctx)
}
