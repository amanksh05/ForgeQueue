package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func generateFingerPrint(payload []byte, jobType string) (string, error) {
	var value any

	if err := json.Unmarshal(payload, &value); err != nil {
		return "", err
	}

	normalized, err := json.Marshal(value)
	if err != nil {
		return "", err
	}

	combined := jobType + ":" + string(normalized)
	fingerprint := sha256.Sum256([]byte(combined))

	return hex.EncodeToString(fingerprint[:]), nil
}

func CreateJob(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := slog.Default()
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
		case "echo", "sleep", "fail_once", "fail":

		default:
			http.Error(w, "Unsupported Job-type", http.StatusBadRequest)
			return
		}

		payload := strings.TrimSpace(string(request.Payload))
		if len(payload) == 0 || payload[0] != '{' || !json.Valid([]byte(payload)) {
			http.Error(w, "Payload must be an JSON object", http.StatusBadRequest)
			return
		}

		idempotencyKey := strings.TrimSpace(
			r.Header.Get("Idempotency-Key"),
		)

		id := uuid.New()

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		fingerPrint, err := generateFingerPrint([]byte(payload), request.Type)
		if err != nil {
			http.Error(w, "Unable to process request payload", http.StatusBadRequest)
			return
		}

		var createdAt time.Time

		if idempotencyKey == "" {
			// No idempotency key: preserve the existing behavior.
			err = pool.QueryRow(ctx, `
        INSERT INTO jobs (id, job_type, payload, status)
        VALUES ($1, $2, $3, 'queued')
        RETURNING created_at
    `, id, request.Type, payload).Scan(&createdAt)

		} else {
			// A key exists: store both the key and fingerprint.
			err = pool.QueryRow(ctx, `
        INSERT INTO jobs (
            id, job_type, payload, status,
            idempotency_key, request_fingerprint
        )
        VALUES ($1, $2, $3, 'queued', $4, $5)
        ON CONFLICT (idempotency_key)
            WHERE idempotency_key IS NOT NULL
        DO NOTHING
        RETURNING created_at
    `, id, request.Type, payload,
				idempotencyKey, fingerPrint).Scan(&createdAt)

			if err == pgx.ErrNoRows {
				// The key already exists. Fetch the original job.
				var existingID uuid.UUID
				var existingType, existingStatus string
				var existingFingerprint string

				err = pool.QueryRow(ctx, `
            SELECT id, job_type, status, created_at,
                   request_fingerprint
            FROM jobs
            WHERE idempotency_key = $1
        `, idempotencyKey).Scan(
					&existingID,
					&existingType,
					&existingStatus,
					&createdAt,
					&existingFingerprint,
				)

				if err != nil {
					logger.Error("Failed to retrieve existing job",
						"operation", "retrieve_existing_job",
						"error", err,
					)
					http.Error(w, "Failed to retrieve existing job",
						http.StatusInternalServerError)
					return
				}

				if existingFingerprint != fingerPrint {
					http.Error(w,
						"Idempotency key already used for a different request",
						http.StatusConflict)
					return
				}

				// Same key and same request: return the original job.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":         existingID.String(),
					"type":       existingType,
					"status":     existingStatus,
					"created_at": createdAt,
				})
				return
			}
		}

		if err != nil {
			logger.Error("Failed to create job",
				"operation", "create_job",
				"idempotency_enabled", idempotencyKey != "",
				"error", err,
			)
			http.Error(w, "Failed to create job",
				http.StatusInternalServerError)
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
