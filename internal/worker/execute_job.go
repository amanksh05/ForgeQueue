package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/amanksh05/forgeQueue/internal/store"
)


type SleepPayloadStruct struct {
	DurationSeconds int `json:"duration_seconds"`
}

type EchoPayloadStruct struct {
	Message string `json:"message"`
}

type FailOncePayloadStruct struct {
	FailOnce bool `json:"fail_once"`
}

func DispatchFail(job store.Job) error {
    return fmt.Errorf("intentional failure for job %s", job.ID)
}

func DispatchEcho(job store.Job) error {
	var payload EchoPayloadStruct
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode message payload: %w", err)
	}
	if payload.Message == "" {
		return fmt.Errorf("No message for this echo job")
	}
	log.Printf("Echo job %s: %s", job.ID, payload.Message)
	return nil
}

func DispatchSleep(ctx context.Context, job store.Job) error {
	var payload SleepPayloadStruct
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode sleep payload: %w", err)
	}

	if payload.DurationSeconds < 0 {
		return fmt.Errorf("duration_seconds can't be negative")
	}

	log.Printf("Sleeping for: %d seconds...", payload.DurationSeconds)
	timer := time.NewTimer(time.Duration(payload.DurationSeconds) * time.Second)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func DispatchFailOnce(job store.Job) error {
	var payload FailOncePayloadStruct
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode fail_once payload: %w", err)
	}

	if !payload.FailOnce {
		return fmt.Errorf("fail_once must be enabled")
	}

	if job.AttemptCount == 1 {
		return fmt.Errorf("intentional failure for fail_once job")
	}

	log.Printf("Failing on my first attempt: job_id=%s", job.ID)
	return nil

}

func ExecuteJob(ctx context.Context, job store.Job) error {

	switch job.Type {
	case "echo":
		return DispatchEcho(job)

	case "sleep":
		return DispatchSleep(ctx, job)

	case "fail_once":
		return DispatchFailOnce(job)

	case "fail":
    	return DispatchFail(job)

	default:
		return fmt.Errorf("Invalid Job Type!!!")
	}

}
