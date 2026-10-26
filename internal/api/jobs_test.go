package api

import "testing"

func TestGenerateFingerPrint_ObjectKeyOrderDoesNotMatter(t *testing.T) {
	first := []byte(`{"message":"hello","count":1}`)
	second := []byte(`{"count":1,"message":"hello"}`)

	firstFingerprint, err := generateFingerPrint(first, "echo")
	if err != nil {
		t.Fatalf("fingerprint first payload: %v", err)
	}

	secondFingerprint, err := generateFingerPrint(second, "echo")
	if err != nil {
		t.Fatalf("fingerprint second payload: %v", err)
	}

	if firstFingerprint != secondFingerprint {
		t.Fatal("equivalent JSON objects produced different fingerprints")
	}
}

func TestGenerateFingerPrint_JobTypeMatters(t *testing.T) {
	payload := []byte(`{"message":"hello"}`)

	echoFingerprint, err := generateFingerPrint(payload, "echo")
	if err != nil {
		t.Fatalf("fingerprint echo payload: %v", err)
	}

	failFingerprint, err := generateFingerPrint(payload, "fail")
	if err != nil {
		t.Fatalf("fingerprint fail payload: %v", err)
	}

	if echoFingerprint == failFingerprint {
		t.Fatal("different job types produced the same fingerprint")
	}
}
