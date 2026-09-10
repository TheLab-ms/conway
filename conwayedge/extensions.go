package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	maxSwipeBatch  = 100
	maxSpoolEvents = 10000
)

// Goal and the version watermark share one atomic file. Legacy writers can
// still change Goal without erasing the last versioned snapshot's identity.
type goalState struct {
	Version *int64          `json:"version"`
	Fobs    json.RawMessage `json:"fobs"`
	Goal    json.RawMessage `json:"goal"`
}

func decodeJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func parseVersionedGoal(version *int64, fobs []byte) (*int64, []byte, error) {
	// Worker versions must be exactly representable by JavaScript numbers.
	if version == nil || *version < 0 || *version > 9007199254740991 {
		return nil, nil, fmt.Errorf("version must be a nonnegative safe integer")
	}
	body, _, err := parseGoal(fobs)
	return version, body, err
}

// Caller holds e.mu. Keep the shipped array cache format until first versioned use.
func (e *edge) persistGoal(body []byte, version *int64, versionBody []byte) error {
	data := body
	if version != nil {
		data, _ = json.Marshal(goalState{version, versionBody, body})
	}
	return e.persist("goal.json", data)
}

func (e *edge) versionedGoal(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version *int64          `json:"version"`
		Fobs    json.RawMessage `json:"fobs"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || decodeJSON(data, &input) != nil {
		http.Error(w, "invalid versioned goal (limit 16 KiB)", http.StatusBadRequest)
		return
	}
	version, body, err := parseVersionedGoal(input.Version, input.Fobs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.persistenceErr != nil {
		http.Error(w, "storage uncertain; restart required", http.StatusServiceUnavailable)
		return
	}
	if e.version != nil {
		if *version < *e.version || (*version == *e.version && !bytes.Equal(body, e.versionBody)) {
			http.Error(w, "older or conflicting goal version", http.StatusConflict)
			return
		}
		if *version == *e.version {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if err := e.persistGoal(body, version, body); err != nil {
		log.Printf("persist versioned goal: %v", err)
		http.Error(w, "cannot persist goal", http.StatusInternalServerError)
		return
	}
	e.version, e.versionBody = version, body
	e.body, e.etag, _ = parseGoal(body)
	w.WriteHeader(http.StatusNoContent)
}

type swipe struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	Controller string    `json:"controller"`
	Fob        uint32    `json:"fob"`
	Allowed    bool      `json:"allowed"`
}

func validateSpool(events []swipe) error {
	if events == nil || len(events) > maxSpoolEvents {
		return fmt.Errorf("invalid swipe spool size")
	}
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		if event.ID == "" || len(event.ID) > 128 || seen[event.ID] || event.Time.IsZero() || event.Fob == 0 {
			return fmt.Errorf("invalid or duplicate spooled swipe")
		}
		seen[event.ID] = true
	}
	return nil
}

func (e *edge) getSwipes(w http.ResponseWriter, r *http.Request) {
	limit := maxSwipeBatch
	if values, ok := r.URL.Query()["limit"]; ok {
		var err error
		limit, err = strconv.Atoi(values[0])
		if err != nil || len(values) != 1 || limit < 1 || limit > maxSwipeBatch {
			http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
			return
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.persistenceErr != nil {
		http.Error(w, "storage uncertain; restart required", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Events []swipe `json:"events"`
	}{e.swipes[:min(limit, len(e.swipes))]})
}

func (e *edge) ackSwipes(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || decodeJSON(data, &input) != nil || input.IDs == nil || len(input.IDs) > maxSwipeBatch {
		http.Error(w, "expected at most 100 IDs (limit 16 KiB)", http.StatusBadRequest)
		return
	}
	ids := make(map[string]bool, len(input.IDs))
	for _, id := range input.IDs {
		if id == "" || len(id) > 128 {
			http.Error(w, "invalid swipe ID", http.StatusBadRequest)
			return
		}
		ids[id] = true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	remaining := make([]swipe, 0, len(e.swipes))
	for _, event := range e.swipes {
		if !ids[event.ID] {
			remaining = append(remaining, event)
		}
	}
	data, _ = json.Marshal(remaining)
	if err := e.persist("swipe-spool.json", data); err != nil {
		log.Printf("persist swipe acknowledgment: %v", err)
		http.Error(w, "cannot persist acknowledgment", http.StatusInternalServerError)
		return
	}
	e.swipes = remaining
	w.WriteHeader(http.StatusNoContent)
}

func loadSigningSeed(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open signing seed: %w", err)
	}
	defer f.Close()
	seed, err := io.ReadAll(io.LimitReader(f, ed25519.SeedSize+1))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing seed must be exactly 32 raw bytes")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
