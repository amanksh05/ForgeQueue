package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func CreateJob(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var request struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}

		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "Invalid JSON request", http.StatusBadRequest)
			return
		}

		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, "Request must contain one JSON request", http.StatusBadRequest)
			return
		}

		switch request.Type {
		case "echo", "sleep", "fail_once" , "fail":

		default:
			http.Error(w, "Unsupported Job-type", http.StatusBadRequest)
			return
		}

		payload := strings.TrimSpace(string(request.Payload))
		if len(payload) == 0 || payload[0] != '{' || !json.Valid([]byte(payload)) {
			http.Error(w, "Payload must be an JSON object", http.StatusBadRequest)
			return
		}

		id := uuid.New()

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		var createdAt time.Time

		err := pool.QueryRow(ctx, `INSERT INTO jobs (id, job_type, payload, status)
			VALUES ($1, $2, $3, 'queued')
			RETURNING created_at
		`, id, request.Type, payload).Scan(&createdAt)

		if err != nil {
			log.Printf("Failed to create job: %v", err)
			http.Error(w, "Failed to create job", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         id.String(),
			"type":       request.Type,
			"status":     "queued",
			"created_at": createdAt,
		})
	}
}
