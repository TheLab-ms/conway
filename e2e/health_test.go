package e2e

import (
	"net/http"
	"testing"
)

func TestHealth_OK(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Get(env.baseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHealth_LANGate(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	req, err := http.NewRequest("GET", env.baseURL+"/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// CF-Connecting-IP indicates the request came from outside the LAN
	// (i.e. through Cloudflare), so OnlyLAN should reject with 403.
	req.Header.Set("CF-Connecting-IP", "203.0.113.42")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403 from LAN gate, got %d", resp.StatusCode)
	}
}

func TestHealth_DBDown(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Close the db. Subsequent operations on it will return errors,
	// causing ServeHealthProbe's BeginTx to fail and yield a 5xx.
	if err := env.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	resp, err := http.Get(env.baseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 500 || resp.StatusCode >= 600 {
		t.Fatalf("expected 5xx with db down, got %d", resp.StatusCode)
	}
}
