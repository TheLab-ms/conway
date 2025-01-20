package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/gavv/httpexpect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMembersAPI(t *testing.T) {
	db := db.NewTest(t)

	_, err := New(db)
	require.NoError(t, err)

	m, err := New(db) // shouldn't generate a token this time
	require.NoError(t, err)

	var token string
	var count int
	err = db.QueryRow("SELECT token, count(*) FROM api_tokens").Scan(&token, &count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	router := engine.NewRouter(nil)
	m.AttachRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	e := httpexpect.Default(t, server.URL)

	// Empty list
	e.GET("/api/members").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().Array().IsEmpty()

	// Initial creation
	e.PATCH("/api/members/leland@palmer.net").
		WithHeader("Authorization", "Bearer "+token).
		WithJSON(map[string]any{
			"email": "laura@palmer.net",
			"name":  "Laura Palmer",
		}).
		Expect().
		Status(http.StatusNoContent)

	list := e.GET("/api/members").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().Array()

	list.Length().IsEqual(1)
	obj := list.Value(0).Object()
	obj.Value("email").IsEqual("leland@palmer.net")
	obj.Value("id").IsEqual(1)
	obj.Value("leadership").IsEqual(0)
	obj.Value("created").Number().IsInt().Gt(0)

	// Partial update
	e.PATCH("/api/members/leland@palmer.net").
		WithHeader("Authorization", "Bearer "+token).
		WithJSON(map[string]any{
			"email":      "laura@palmer.net",
			"leadership": true,
		}).
		Expect().
		Status(http.StatusNoContent)

	list = e.GET("/api/members").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().Array()

	list.Length().IsEqual(1)
	obj = list.Value(0).Object()
	obj.Value("email").IsEqual("leland@palmer.net")
	obj.Value("id").IsEqual(1)
	obj.Value("leadership").IsEqual(1)
	obj.Value("created").Number().IsInt().Gt(0)

	// Deletion
	e.DELETE("/api/members/leland@palmer.net").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusNoContent)

	e.DELETE("/api/members/leland@palmer.net").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusNotFound)

	// Empty list
	e.GET("/api/members").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().Array().IsEmpty()
}
