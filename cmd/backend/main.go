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
	port := getenv("PORT", "3000")
	configLatency, _ := strconv.Atoi(getenv("LATENCY_MS", "0"))
	errorRate, _ := strconv.ParseFloat(getenv("ERROR_RATE", "0"), 64)

	handler := func(w http.ResponseWriter, r *http.Request) {
		isError := rand.Float64() < errorRate
		requestLatency := int(rand.Float64() * float64(configLatency))
		time.Sleep(time.Duration(requestLatency) * time.Millisecond)

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

	http.HandleFunc("/api", handler)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	log.Printf("Server listening on %s\n", port)
	if err := http.ListenAndServe(":"+port, http.DefaultServeMux); err != nil {
		log.Fatalf("HTTP server error: %v\n", err)
	}
}
