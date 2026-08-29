package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	// yaml parsing
	backendURL, err := url.Parse("http://localhost:3000")
	if err != nil {
		log.Fatalf("Error parsing the url: %v\n", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	proxy.ModifyResponse = func(resp *http.Response) error {
		method := resp.Request.Method
		status := resp.StatusCode
		path := resp.Request.URL.Path
		log.Printf("Method: %v | StatusCode: %v | Path: %v\n", method, status, path)
		return nil
	}
	// graceful shutdown on ctrl+c
	// error handler

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	http.ListenAndServe(":8080", http.DefaultServeMux)

}
