package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/amanksh05/forgeQueue/internal/api"
	"github.com/amanksh05/forgeQueue/internal/store"
	"github.com/amanksh05/forgeQueue/internal/worker"
	"github.com/joho/godotenv"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil {
		logger.Info("No .env file found; using environment variables")
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	pool, err := store.NewPool(ctx)
	if err != nil {
		logger.Error("Failed to connect to PostgreSQL", "error", err)
		return
	}
	defer pool.Close()

	logger.Info("Connected to PostgreSQL")

	router := http.NewServeMux()
	router.HandleFunc("/health", health)
	router.HandleFunc("POST /jobs", api.CreateJob(pool))
	router.HandleFunc("GET /jobs/{id}", api.GetJob(pool))
	router.HandleFunc("GET /jobs", api.ListJobs(pool))
	router.HandleFunc("POST /claim", api.ClaimJob(pool))

	server := &http.Server{
		Addr:              ":8080",
		Handler:           loggingMiddleware(router),
		ReadHeaderTimeout: 5 * time.Second,
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		worker.RunWorker(ctx, pool)
	}()

	serverErr := make(chan error, 1)

	go func() {
		logger.Info("HTTP server starting", "address", server.Addr)

		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}

		serverErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("Shutdown signal received")

	case err := <-serverErr:
		if err != nil {
			logger.Error("HTTP server stopped unexpectedly", "error", err)
		} else {
			logger.Info("HTTP server stopped")
		}
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	logger.Info("Shutting down HTTP server")

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Graceful HTTP shutdown failed", "error", err)

		if closeErr := server.Close(); closeErr != nil {
			logger.Error("Force-closing HTTP server failed", "error", closeErr)
		}
	} else {
		logger.Info("HTTP server shut down successfully")
	}

	wg.Wait()
	logger.Info("Worker has stopped")
	logger.Info("ForgeQueue shutdown complete")
}

func loggingMiddleware(next http.Handler) http.Handler {
	logger := slog.Default()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
		}

		next.ServeHTTP(sw, r)

		logger.Info("HTTP request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}

	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	return w.ResponseWriter.Write(data)
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}
