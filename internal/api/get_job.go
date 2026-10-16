package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func GetJob(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idString := r.PathValue("id")

		id, err := uuid.Parse(idString)
		if err != nil {
			http.Error(w, "Failed to parse id", http.StatusBadRequest)
			return
		}

		var request struct {
			ID        string          `json:"id"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
			Status    string          `json:"status"`
			CreatedAt time.Time       `json:"created_at"`
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		err = pool.QueryRow(ctx,
			`SELECT id, job_type, payload, status, created_at FROM JOBS WHERE id = $1`, id).Scan(
			&request.ID,
			&request.Type,
			&request.Payload,
			&request.Status,
			&request.CreatedAt,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, "Job not found", http.StatusNotFound)
				return
			}
			log.Printf("Failed to fetch job: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(request)
		if err != nil {
			log.Printf("encode error %v", err)
		}

	}
}
