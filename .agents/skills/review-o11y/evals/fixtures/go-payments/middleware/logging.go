package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		fmt.Printf("[REQ] %s %s headers=%v\n", r.Method, r.URL.Path, r.Header)
		next.ServeHTTP(w, r)
		slog.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", time.Since(start).Milliseconds())
	})
}
