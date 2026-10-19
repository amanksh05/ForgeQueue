package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/amanksh05/forgeQueue/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ClaimJob(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		job, err := store.ClaimJob(ctx, pool)

		if errors.Is(err, store.ErrorNoJobAvailable) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			log.Printf("Error claim_job: %v", err)
			http.Error(w, "Failed to claim the job", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(job); err != nil {
			log.Printf("ERROR claim_job: encode response failed: %v", err)
		}

	}
}
