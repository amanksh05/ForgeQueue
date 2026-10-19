package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"time"
)

func ClaimJob(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		claimToken := uuid.New()

		var job struct {
			ID             string          `json:"id"`
			Type           string          `json:"type"`
			Payload        json.RawMessage `json:"payload"`
			Status         string          `json:"status"`
			CreatedAt      time.Time       `json:"created_at"`
			ClaimToken     uuid.UUID       `json:"claim_token"`
			LeaseExpiresAt time.Time       `json:"lease_expires_at"`
		}

		log.Printf("INFO claim_job: request started method=%s path=%s",
			r.Method, r.URL.Path)

		tx, err := pool.Begin(ctx)
		if err != nil {
			log.Printf("ERROR claim_job: begin transaction failed: %v", err)
			http.Error(w, "Failed to claim job", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(context.Background())

		err = tx.QueryRow(ctx, `
			SELECT id, job_type, payload, status, created_at
			FROM jobs
			WHERE status = 'queued'
			AND available_at <= NOW()
			ORDER BY available_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`).Scan(
			&job.ID,
			&job.Type,
			&job.Payload,
			&job.Status,
			&job.CreatedAt,
		)

		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Print("INFO claim_job: no eligible jobs available")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			log.Printf("ERROR claim_job: select job failed: %v", err)
			http.Error(w, "Failed to fetch job", http.StatusInternalServerError)
			return
		}

		log.Printf("INFO claim_job: job selected id=%s", job.ID)

		err = tx.QueryRow(ctx, `
			UPDATE jobs
			SET status = 'running'
			, claim_token = $1
			, lease_expires_at = NOW() + INTERVAL '30 seconds'
			WHERE id = $2 AND status = 'queued'
			RETURNING status, claim_token, lease_expires_at
		`, claimToken, job.ID).Scan(
			&job.Status,
			&job.ClaimToken,
			&job.LeaseExpiresAt,
		)

		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Printf("No job was updated for job_id=%s", job.ID)
				http.Error(w, "Job could not be claimed", http.StatusConflict)
				return
			}
			log.Printf("ERROR claim_job: update failed job_id=%s err=%v", job.ID, err)
			http.Error(w, "Failed to claim job", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(ctx); err != nil {
			log.Printf("ERROR claim_job: commit failed job_id=%s err=%v",
				job.ID, err)
			http.Error(w, "Failed to claim job", http.StatusInternalServerError)
			return
		}

		log.Printf("INFO claim_job: job claimed successfully job_id=%s",
			job.ID)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(job); err != nil {
			log.Printf("ERROR claim_job: encode response failed job_id=%s err=%v",
				job.ID, err)
			return
		}
	}
}
