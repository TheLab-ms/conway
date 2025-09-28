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
