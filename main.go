package main

import (
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"context"
	"net/http"

	"github.com/amanksh05/forgeQueue/internal/api"
	"github.com/amanksh05/forgeQueue/internal/store"
	"github.com/amanksh05/forgeQueue/internal/worker"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found; using environment variables")
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	pool, err := store.NewPool(ctx)
	if err != nil {
		log.Fatal(err)
	}

	defer pool.Close()
	log.Println("Connected to PostgreSQL")
	router := http.NewServeMux()
	router.HandleFunc("/health", health)
	router.HandleFunc("POST /jobs", api.CreateJob(pool))
	router.HandleFunc("GET /jobs/{id}", api.GetJob(pool))
	router.HandleFunc("GET /jobs", api.ListJobs(pool))
	router.HandleFunc("POST /claim", api.ClaimJob(pool))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.RunWorker(ctx, pool)
	}()

	log.Println("Forgqueue is listening on port :8080")

	server := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}
	go func() {
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
	<-ctx.Done()
	log.Println("Shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Graceful shutdown failed: %v", err)
	}

	wg.Wait()
	log.Println("Worker stopped")
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})

}
