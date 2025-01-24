package oauth2

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/gavv/httpexpect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootDomain(t *testing.T) {
	assert.Equal(t, "bar.baz", rootDomain(&url.URL{Host: "foo.bar.baz:8080"}))
	assert.Equal(t, "bar.baz", rootDomain(&url.URL{Host: "foo.bar.baz"}))
	assert.Equal(t, "baz", rootDomain(&url.URL{Host: "baz"}))
}

func TestUserInfo(t *testing.T) {
	db := db.NewTest(t)
	am, err := auth.New(db, &url.URL{}, nil)
	require.NoError(t, err)
	m := New(db, &url.URL{}, am)

	router := engine.NewRouter(nil)
	m.AttachRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	e := httpexpect.Default(t, server.URL)

	// Basic active user
	_, err = db.Exec("INSERT INTO members (email, name, confirmed, non_billable) VALUES ('foo', 'bar', 1, 1)")
	require.NoError(t, err)

	token, err := m.signToken(1, "baz")
	require.NoError(t, err)

	e.GET("/oauth2/userinfo").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().IsEqual(map[string]any{
		"email":  "foo",
		"groups": []string{"member"},
		"id":     "6b86b273-ff34-fce1-9d6b-804eff5a3f57",
		"name":   "bar",
	})

	// Inactive user
	_, err = db.Exec("INSERT INTO members (email, name, confirmed, non_billable) VALUES ('inactive', 'inactive member', 0, 1)")
	require.NoError(t, err)

	token, err = m.signToken(2, "baz")
	require.NoError(t, err)

	e.GET("/oauth2/userinfo").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().IsEqual(map[string]any{
		"email":  "inactive",
		"groups": []string{},
		"id":     "d4735e3a-265e-16ee-e03f-59718b9b5d03",
		"name":   "inactive member",
	})

	// Leadership user
	_, err = db.Exec("INSERT INTO members (email, name, leadership, confirmed, non_billable) VALUES ('leadership', 'leadership user', 1, 1, 1)")
	require.NoError(t, err)

	token, err = m.signToken(3, "baz")
	require.NoError(t, err)

	e.GET("/oauth2/userinfo").
		WithHeader("Authorization", "Bearer "+token).
		Expect().
		Status(http.StatusOK).JSON().IsEqual(map[string]any{
		"email":  "leadership",
		"groups": []string{"member", "admin"},
		"id":     "4e074085-62be-db8b-60ce-05c1decfe3ad",
		"name":   "leadership user",
	})
}
