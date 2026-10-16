package api

import (
	// "encoding/json"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type job struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Status    string          `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
}

func ListJobs(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 10
		offset := 0
		status := ""

		if limitString := r.URL.Query().Get("limit"); limitString != "" {

			value, err := strconv.Atoi(limitString)
			if err != nil || value <= 0 {
				log.Printf("Invalid limit parameter: %q", limitString)
				http.Error(w, "Invalid  limit", http.StatusBadRequest)
				return
			}

			limit = value
		}

		if offsetString := r.URL.Query().Get("offset"); offsetString != "" {

			value, err := strconv.Atoi(offsetString)
			if err != nil || value < 0 {
				log.Printf("Invalid offset parameter: %q", offsetString)
				http.Error(w, "Invalid  offset parameter", http.StatusBadRequest)
				return
			}

			offset = value
		}

		if statusString := r.URL.Query().Get("status"); statusString != "" {
			switch statusString {
			case "queued", "running", "retry_wait", "succeeded", "dead":

			default:
				http.Error(w, "Invalid status-type", http.StatusBadRequest)
				return
			}
			status = statusString
		}

		if limit > 100 {
			limit = 100
		}

		rows, err := pool.Query(r.Context(),
			`SELECT id, job_type, payload, status, created_at
			FROM jobs
			WHERE ($1 = '' OR status = $1)
			ORDER BY created_at DESC, id DESC
			LIMIT $2 OFFSET $3; `, status, limit, offset,
		)

		if err != nil {
			log.Print("Unable to fetch jobs")
			http.Error(w, "Unable to fetch jobs from DB.", http.StatusInternalServerError)
			return
		}

		defer rows.Close()
		jobs := make([]job, 0)
		for rows.Next() {
			var j job
			err := rows.Scan(
				&j.ID,
				&j.Type,
				&j.Payload,
				&j.Status,
				&j.CreatedAt,
			)
			if err != nil {
				log.Printf("Failed to scan job row: %v", err)
				http.Error(w, "Internal server error",
					http.StatusInternalServerError)
				return
			}
			jobs = append(jobs, j)

		}
		if err := rows.Err(); err != nil {
			log.Printf("Error while iterating job rows: %v", err)
			http.Error(w, "Internal server error",
				http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		err = json.NewEncoder(w).Encode(jobs)

		if err != nil {
			log.Printf("Failed to encode jobs response: %v", err)
			return
		}

	}
}
