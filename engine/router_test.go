package engine

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRouter(t *testing.T) {
	router := NewRouter()

	t.Run("404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/missing", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, "404 page not found\n", w.Body.String())
	})

	t.Run("htmx assets", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/assets/htmx.min.js", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, 200, w.Result().StatusCode)
		assert.Equal(t, "text/javascript; charset=utf-8", w.Header().Get("Content-Type"))
	})
}
