package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.yaml.in/yaml/v4"
)

type Config struct {
	NetworkTimeoutsSec struct {
		Transport struct {
			TCP            int `yaml:"tcp"`
			KeepAlive      int `yaml:"keep_alive"`
			TLS            int `yaml:"tls"`
			ResponseHeader int `yaml:"response_header"`
			IdleConn       int `yaml:"idle_conn"`
		} `yaml:"transport"`

		Server struct {
			Context    int `yaml:"context"`
			ReadHeader int `yaml:"read_header"`
			Idle       int `yaml:"idle"`
		} `yaml:"server"`
	} `yaml:"network_timeouts_s"`

	Upstreams []struct {
		Name    string `yaml:"name"`
		Url     string `yaml:"url"`
		Timeout int    `yaml:"timeout_ms"`
	} `yaml:"upstreams"`

	Routes []struct {
		Match struct {
			PathPrefix string `yaml:"path_prefix"`
		} `yaml:"match"`
		Upstream string `yaml:"upstream"`
		Mirror   struct {
			Upstream string  `yaml:"upstream"`
			Ratio    float64 `yaml:"ratio"`
		} `yaml:"mirror"`
	} `yaml:"routes"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	configPath := "config.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Error reading config (%s): %v\n", configPath, err)
	}

	var config Config
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		log.Fatalf("Failed yaml umarshal (%s): %v\n", configPath, err)
	}

	log.Printf("CONFIG: %v\n", config)

	// TODO: multiple backends (3001/3002/3003)
	// TODO: route policy
	// TODO: hot reload
	backendURL, err := url.Parse(config.Upstreams[0].Url)
	if err != nil {
		log.Fatalf("Error parsing the url: %v\n", err)
	}

	toSec := func(d int) time.Duration { return time.Duration(d) * time.Second }
	timeoutsTransport := config.NetworkTimeoutsSec.Transport
	timeoutsServer := config.NetworkTimeoutsSec.Server

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// Setting headers to prevent spoofing
			// do RESEARCH on that
			r.Out.Header.Del("X-Forwarded-For")
			clientIP, _, err := net.SplitHostPort(r.In.RemoteAddr)
			if err != nil {
				log.Fatalf("Failed splitting client IP during Rewrite: %v\n", err)
			}
			r.Out.Header.Set("X-Forwarded-For", clientIP)

			if r.In.TLS != nil {
				r.Out.Header.Set("X-Forwarded-Proto", "https")
			} else {
				r.Out.Header.Set("X-Forwarded-Proto", "http")
			}

			r.SetURL(backendURL)
			r.Out.Host = backendURL.Host
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("HTTP error on %s %s: %v\n", r.Method, r.URL.Path, err)
			if errors.Is(err, context.DeadlineExceeded) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGatewayTimeout)
				_, err = w.Write([]byte(`{"error": "Requested timed out on the server"}`))
				if err != nil {
					log.Fatalf("[ErrorHandler]: failed to write response: %v\n", err)
				}
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, err = w.Write([]byte(`{"error": "An unexpected error occurred"}`))
			if err != nil {
				log.Fatalf("[ErrorHandler]: failed to write response: %v\n", err)
			}
		},

		ModifyResponse: func(resp *http.Response) error {
			method := resp.Request.Method
			status := resp.StatusCode
			path := resp.Request.URL.Path
			log.Printf("Method: %v | StatusCode: %v | Path: %v\n", method, status, path)
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

	server := &http.Server{
		Addr: ":8080",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), toSec(timeoutsServer.Context))
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(ctx))
		}),
		ReadHeaderTimeout: toSec(timeoutsServer.ReadHeader),
		IdleTimeout:       toSec(timeoutsServer.Idle),
	}

	go func() {
		log.Println("Proxy running at :8080")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server error: %v\n", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Shutdown failed: %v\n", err)
	}

	// clean up will be here
	log.Println("Server stopped")

}
