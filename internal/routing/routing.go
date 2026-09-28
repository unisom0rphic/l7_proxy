package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/unisom0rphic/l7proxy/internal/config"
)

type Router struct {
	nameToHost   map[string]*url.URL
	configPath   string
	atomicConfig atomic.Pointer[config.Config]
}

// Decides which API route to use for a given http.Request
func (router *Router) DecideRoute(r *http.Request) (*url.URL, error) {
	path := r.URL.Path
	if path == "" {
		return nil, errors.New("empty url")
	}
	slog.Debug("decideRoute: received input", "path", path)
	candidates := make([]*url.URL, 0)

	// Path: if found exact match - return immediately
	for _, route := range router.Config().Routes {
		routePath := route.Rule.Path
		if routePath == path {
			name := route.Upstream
			host, ok := router.nameToHost[name]

			if !ok {
				slog.Debug("decidePath: route not found")
				return nil, errors.New("route not found")
			}
			slog.Debug("decidePath: route found", "host", host)

			// Method check
			if method := route.Rule.Method; method != "" {
				if method != r.Method {
					return nil, errors.New("Method mismatch")
				}
			}

			// Headers existence
			if header := route.Rule.Header; header != "" {
				if _, ok := r.Header[header]; !ok {
					return nil, errors.New("Header not found")
				}
			}

			url := &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
				Path:   path,
			}
			return url, nil
		}
	}

	// Prefix
	for _, route := range router.Config().Routes {
		prefix := route.Rule.PathPrefix
		if prefix == "" {
			continue
		}

		rewrite := route.Rule.Rewrite
		// Use rule`s `path` if `rewrite` is empty
		if rewrite == "" {
			rewrite = route.Rule.Path
		}

		if strings.HasPrefix(path, prefix) {
			slog.Debug("found prefix", "prefix", prefix, "path", path)
			upstreamService := route.Upstream
			host, ok := router.nameToHost[upstreamService]

			if !ok {
				// If we reach this branch it means the config was incorrectly parsed,
				// which means serious issue inside the parser that bypasses the tests.
				// TODO: consider panicking when config validator ensures each name is mapped
				// to the host.
				slog.Error("decideRoute: host not found in upstreams",
					"upstream", upstreamService, "prefix", prefix, "path", path)
				return nil, errors.New("URL host not mapped during config parse")
			}

			// Method check
			if method := route.Rule.Method; method != "" && method != r.Method {
				continue
			}

			// Headers existence
			if header := route.Rule.Header; header != "" {
				if _, ok := r.Header[header]; !ok {
					continue
				}
			}

			url := &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
				Path:   rewrite,
			}
			candidates = append(candidates, url)
		}
	}

	if len(candidates) == 0 {
		slog.Debug("decideRoute: no match", "path", path)
		return nil, errors.New("route not found")
	}

	// Looking for the longest URL
	maxLen := 0
	var bestCandidate *url.URL
	for _, candidate := range candidates {
		if len(candidate.Path) > maxLen {
			maxLen = len(candidate.Path)
			bestCandidate = candidate
		}
	}

	return bestCandidate, nil
}

func CreateFromConfig(ctx context.Context, configPath string) (*Router, error) {
	cfg, err := config.ParseConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	router, err := NewRouter(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create router from config: %w", err)
	}
	router.configPath = configPath

	router.startMonitoringConfigUpdates(ctx)

	return router, nil
}

// Maps upstream names to their hosts (e.g users-prod -> http://users-prod:3001)
func mapNamesToHosts(cfg *config.Config) (map[string]*url.URL, error) {
	nameToHost := make(map[string]*url.URL)
	for _, upstream := range cfg.Upstreams {
		name := upstream.Name
		host := upstream.Host

		url, err := url.Parse(host)
		if !slices.Contains([]string{"http", "https"}, url.Scheme) || url.Scheme == "" {
			return nil, fmt.Errorf("Error parsing config url for %s: incorrect scheme: %s", name, host)
		}
		if err != nil {
			return nil, fmt.Errorf("error mapping upstream %s to host %s, %w", name, host, err)
		}
		nameToHost[name] = url
	}

	return nameToHost, nil
}

func NewRouter(cfg *config.Config) (*Router, error) {
	router := &Router{}
	router.swapConfig(cfg)
	nameToHost, err := mapNamesToHosts(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating new router failed during upstream->host mapping: %w", err)
	}
	router.nameToHost = nameToHost
	return router, nil
}

// Starts monitoring config file for updates, automatically swaps if notices changes.
// Panics on config file removal.
func (router *Router) startMonitoringConfigUpdates(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create a new watcher: %w", err)
	}

	absTarget, err := filepath.Abs(router.configPath)
	if err != nil {
		return fmt.Errorf("failed to obtain absolute path: %w", err)
	}

	parentDir := filepath.Dir(absTarget)

	// Listener loop
	go func() {
		defer watcher.Close()

		configCooldown := time.NewTimer(0)
		if !configCooldown.Stop() {
			<-configCooldown.C
		}
		defer configCooldown.Stop()

		onCoolDown := false

		for {
			select {
			case <-ctx.Done():
				slog.Info("monitor shutting down due to context cancellation")
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				absEventPath, err := filepath.Abs(event.Name)
				if err != nil {
					continue
				}

				if absEventPath != absTarget {
					continue
				}

				if event.Has(fsnotify.Write) {
					if onCoolDown {
						continue
					}
					slog.Info("config modified", "file", event.Name)
					router.updateConfig()
					onCoolDown = true
					configCooldown.Reset(1 * time.Second)
				} else if event.Has(fsnotify.Remove) {
					panic(fmt.Sprintf("Config deleted: %s", event.Name))
				}
			case <-configCooldown.C:
				onCoolDown = false
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("watcher error", "error", err)
			}

		}
	}()

	err = watcher.Add(parentDir)
	if err != nil {
		return fmt.Errorf("adding directory (%s) to watch list failed: %w", parentDir, err)
	}
	slog.Info("monitoring changes", "config", router.configPath)

	return nil
}

// Used to apply changes in router.configPath
func (router *Router) updateConfig() error {
	cfg, err := config.ParseConfig(router.configPath)
	if err != nil {
		return fmt.Errorf("error updating config: %w", err)
	}
	router.swapConfig(cfg)
	nameToHost, err := mapNamesToHosts(cfg)
	if err != nil {
		return fmt.Errorf("updating router config failed during upstream->host mapping: %w", err)
	}
	router.nameToHost = nameToHost
	return nil
}

func (router *Router) swapConfig(cfg *config.Config) {
	router.atomicConfig.Store(cfg)
}

func (router *Router) Config() *config.Config {
	return router.atomicConfig.Load()
}
