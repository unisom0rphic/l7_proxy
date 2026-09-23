package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func getenv(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	serviceName := getenv("SERVICE_NAME", "unnamed")
	endpoint := "/api/" + serviceName
	port := getenv("PORT", "3000")
	configLatency, _ := strconv.Atoi(getenv("LATENCY_MS", "0"))
	errorRate, _ := strconv.ParseFloat(getenv("ERROR_RATE", "0"), 64)

	handler := func(w http.ResponseWriter, r *http.Request) {
		isError := rand.Float64() < errorRate
		time.Sleep(time.Duration(configLatency) * time.Millisecond)

		log.Printf("%s: received headers: %v\n", serviceName, r.Header)

		if isError {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "%s: internal error\n", serviceName)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%s: success\n", serviceName)
	}

	http.HandleFunc(endpoint, handler)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	http.HandleFunc("/hang", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Second)
	})
	http.HandleFunc("/hang-body", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("part"))
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Second)
	})

	log.Printf("Server listening on %s\n", port)
	if err := http.ListenAndServe(":"+port, http.DefaultServeMux); err != nil {
		log.Fatalf("HTTP server error: %v\n", err)
	}
}
