package routing

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/unisom0rphic/l7proxy/internal/config"
)

type Router struct {
	NameToHost map[string]*url.URL
	// TODO: hot reload
	Config *config.Config
}

// Decides which API route to use given a path
//
// # Don't change signature
// also prob need Router struct
func (router *Router) DecideRoute(path string) (*url.URL, error) {
	// TODO: obviously add support for other rules
	log.Printf("[decideRoute]: Received input: %v\n", path)

	// Path
	for _, route := range router.Config.Routes {
		routePath := route.Rule.Path
		if routePath == path {
			name := route.Upstream
			host, ok := router.NameToHost[name]

			if !ok {
				log.Println("[decidePath]: Route not found")
				return nil, errors.New("Route not found")
			}

			// TODO: maybe create a method to create a url given
			// host and path?
			return &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
				Path:   "/api",
			}, nil
		}
	}

	// Prefix
	for _, route := range router.Config.Routes {
		prefix := route.Rule.PathPrefix
		if prefix == "" {
			continue
		}
		// FIXME: should be different logic, will catch /usersfoo for /users
		if strings.HasPrefix(path, prefix) {
			log.Printf("Found prefix: %v for %v\n", prefix, path)
			upstreamService := route.Upstream
			host, ok := router.NameToHost[upstreamService]

			if !ok {
				log.Printf(
					"[decideRoute]: host not found in upstreams\nHost %v\nUpstream %v\n",
					host, upstreamService)
				return nil, errors.New("Unknown host URL")
			}

			url := &url.URL{
				Scheme: host.Scheme,
				Host:   host.Host,
				// Path:   path, // strip prefix or some
				Path: "/api",
			}

			return url, nil
		}
	}

	// Headers
	// no idea like we should pass r.In and look at headers?
	// the same with method and query parameters

	log.Printf("[decideRoute]: No match for %v\n", path)
	return nil, errors.New("Route not found")
}

func CreateFromConfig(configPath string) (*Router, error) {
	cfg, err := config.ParseConfig(configPath)

	if err != nil {
		return nil, fmt.Errorf("Failed to parse config: %v\n", err)
	}

	router, err := NewRouter(cfg)
	if err != nil {
		return nil, fmt.Errorf("Failed to create router from config: %v\n", err)
	}
	return router, nil
}

func NewRouter(cfg *config.Config) (*Router, error) {
	router := &Router{}
	router.Config = cfg

	nameToHost := make(map[string]*url.URL)
	for _, upstream := range cfg.Upstreams {
		name := upstream.Name
		host := upstream.Host

		url, err := url.Parse(host)
		if err != nil {
			return nil, fmt.Errorf("Error parsing host url from config: %v\n", err)
		}
		nameToHost[name] = url
	}
	router.NameToHost = nameToHost
	return router, nil
}
