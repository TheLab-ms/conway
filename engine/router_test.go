package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRouter(t *testing.T) {
	router := NewRouter(nil)
	assert.NotNil(t, router)
	assert.NotNil(t, router.router)
	assert.NotNil(t, router.Authenticator)

	// Test with custom handler
	customHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not found"))
	})
	router = NewRouter(customHandler)
	req := httptest.NewRequest("GET", "/missing", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, "not found", w.Body.String())
}

func TestRouter_Handle(t *testing.T) {
	router := NewRouter(nil)

	// Basic request handling
	router.Handle("GET", "/test", func(r *http.Request) Response {
		return JSON(map[string]string{"ok": "true"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"ok":"true"`)

	// Path parameters
	router.Handle("GET", "/users/{id}", func(r *http.Request) Response {
		return JSON(map[string]string{"id": r.PathValue("id")})
	})

	req = httptest.NewRequest("GET", "/users/123", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Contains(t, w.Body.String(), `"id":"123"`)

	// Error handling - JSON
	router.Handle("GET", "/error", func(r *http.Request) Response {
		return ClientErrorf(http.StatusBadRequest, "bad request")
	})

	req = httptest.NewRequest("GET", "/error", nil)
	req.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "bad request")
}
