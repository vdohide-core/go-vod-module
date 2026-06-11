package server

import (
	"net/http"
	"sync/atomic"
)

var (
	TotalRequests int64
	TotalHits     int64
	TotalMisses   int64
)

// MetricsMiddleware tracks requests locally by incrementing the requests counter
func MetricsMiddleware(next http.Handler, pathPattern string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		atomic.AddInt64(&TotalRequests, 1)
	}
}
