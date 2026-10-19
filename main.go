package main

import (
	"encoding/json"
	"log"

	"context"
	"net/http"

	"github.com/amanksh05/forgeQueue/internal/store"
	"github.com/amanksh05/forgeQueue/internal/api"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil{
		log.Println("No .env file found; using environment variables")
	}
	ctx := context.Background()
	pool, err := store.NewPool(ctx)
	if err != nil{
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

	log.Println("Forgqueue is listening on port :8080")
	err = http.ListenAndServe(":8080", router)
	if err != nil {
		log.Fatal("Failed to initiate Forgequeue. ERROR:", err)
		return
	}

}

func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})

}
