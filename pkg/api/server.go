package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// ServerConfig holds server configuration.
type ServerConfig struct {
	Addr           string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxConcurrent  int
	CORSOrigin     string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig(addr string) ServerConfig {
	return ServerConfig{
		Addr:          addr,
		ReadTimeout:   5 * time.Second,
		WriteTimeout:  5 * time.Second,
		MaxConcurrent: runtime.NumCPU() * 2,
		CORSOrigin:    "",
	}
}

// NewServer creates an HTTP server with all routes and middleware.
func NewServer(cfg ServerConfig, handlers *Handlers) *http.Server {
	mux := http.NewServeMux()

	// Concurrency limiter.
	sem := make(chan struct{}, cfg.MaxConcurrent)

	// Routes.
	mux.HandleFunc("POST /api/v1/route", withMiddleware(handlers.HandleRoute, sem, cfg))
	mux.HandleFunc("GET /api/v1/health", withMiddleware(handlers.HandleHealth, sem, cfg))
	mux.HandleFunc("GET /api/v1/stats", withMiddleware(handlers.HandleStats, sem, cfg))

	return &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
}

// ListenAndServe starts the server and blocks until shutdown signal.
func ListenAndServe(srv *http.Server) error {
	// Graceful shutdown on SIGTERM/SIGINT.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Server listening on %s", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Printf("Received %s, shutting down...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}

// withMiddleware wraps a handler with logging, recovery, security headers,
// and concurrency limiting.
func withMiddleware(handler http.HandlerFunc, sem chan struct{}, cfg ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Security headers.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")

		// CORS.
		if cfg.CORSOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", cfg.CORSOrigin)
		}

		// Concurrency limiter.
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"service_unavailable"}`, http.StatusServiceUnavailable)
			return
		}

		// Recovery.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
			}
		}()

		// Request timeout.
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		start := time.Now()
		handler(w, r.WithContext(ctx))
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Microsecond))
	}
}
