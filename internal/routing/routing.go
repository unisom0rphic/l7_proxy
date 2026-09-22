package routing

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	AtomicConfig atomic.Pointer[config.Config]
}

// Decides which API route to use given a path
//
// # Don't change signature
func (router *Router) DecideRoute(path string) (*url.URL, error) {
	// TODO: obviously add support for other rules
	log.Printf("[decideRoute]: Received input: %v\n", path)

	// Path
	for _, route := range router.AtomicConfig.Load().Routes {
		routePath := route.Rule.Path
		if routePath == path {
			name := route.Upstream
			host, ok := router.nameToHost[name]

			if !ok {
				log.Println("[decidePath]: Route not found")
				return nil, errors.New("route not found")
			}

			// TODO: maybe create a method to create a url given
			// host and path?
			return &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
			}, nil
		}
	}

	// Prefix
	for _, route := range router.AtomicConfig.Load().Routes {
		prefix := route.Rule.PathPrefix
		if prefix == "" {
			continue
		}
		// FIXME: should be different logic, will catch /usersfoo for /users
		if strings.HasPrefix(path, prefix) {
			log.Printf("Found prefix: %v for %v\n", prefix, path)
			upstreamService := route.Upstream
			host, ok := router.nameToHost[upstreamService]

			if !ok {
				log.Printf(
					"[decideRoute]: host not found in upstreams\nHost %v\nUpstream %v\n",
					host, upstreamService)
				return nil, errors.New("unknown host URL")
			}

			url := &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
				// Path:   path, // strip prefix or some
			}

			return url, nil
		}
	}

	// Headers
	// no idea like we should pass r.In and look at headers?
	// the same with method and query parameters

	log.Printf("[decideRoute]: No match for %v\n", path)
	return nil, errors.New("route not found")
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
				log.Printf("it's me monitor I'm dying cause of context")
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
					log.Printf("Config modified: %s", event.Name)
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
				log.Println("Watcher error: ", err)
			}

		}
	}()

	err = watcher.Add(parentDir)
	if err != nil {
		return fmt.Errorf("adding directory (%s) to watch list failed: %w", parentDir, err)
	}
	log.Println("Monitoring changes for ", router.configPath)

	return nil
}

// Used to apply changes in router.configPath
func (router *Router) updateConfig() error {
	cfg, err := config.ParseConfig(router.configPath)
	if err != nil {
		return fmt.Errorf("error updating config: %w", err)
	}
	router.AtomicConfig.Store(cfg)
	nameToHost, err := mapNamesToHosts(cfg)
	if err != nil {
		return fmt.Errorf("updating router config failed during upstream->host mapping: %w", err)
	}
	router.nameToHost = nameToHost
	return nil
}

func (router *Router) swapConfig(cfg *config.Config) {
	router.AtomicConfig.Store(cfg)
}

func (router *Router) GetConfig() *config.Config {
	return router.AtomicConfig.Load()
}
