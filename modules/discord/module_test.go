package discord

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type testHarness struct {
	db     *sql.DB
	module *Module
	server *httptest.Server
	t      *testing.T
}

func newTestHarness(t *testing.T) *testHarness {
	testDB := db.NewTest(t)
	selfURL, _ := url.Parse("http://localhost:8080")

	server := httptest.NewServer(mockDiscordAPI())
	module := New(testDB, selfURL, nil, "client-id", "client-secret", "bot-token", "guild-id", "role-id")
	module.client.client = &http.Client{Transport: &mockTransport{server: server}}

	return &testHarness{
		db:     testDB,
		module: module,
		server: server,
		t:      t,
	}
}

func (h *testHarness) close() {
	h.server.Close()
}

func (h *testHarness) createMember(id int, discordUserID, email string, confirmed bool) {
	confirmedVal := 0
	if confirmed {
		confirmedVal = 1
	}
	_, err := h.db.Exec(`
		INSERT INTO members (id, discord_user_id, email, confirmed, waiver, fob_id, stripe_subscription_state)
		VALUES (?, ?, ?, ?, 1, ?, 'active')
	`, id, discordUserID, email, confirmedVal, 100+id)
	require.NoError(h.t, err)
}

func (h *testHarness) memberSyncTime(id int) *int64 {
	var syncTime sql.NullInt64
	err := h.db.QueryRow("SELECT discord_last_synced FROM members WHERE id = ?", id).Scan(&syncTime)
	require.NoError(h.t, err)
	if !syncTime.Valid {
		return nil
	}
	return &syncTime.Int64
}

func (h *testHarness) memberDiscordID(id int) string {
	var discordID sql.NullString
	err := h.db.QueryRow("SELECT discord_user_id FROM members WHERE id = ?", id).Scan(&discordID)
	require.NoError(h.t, err)
	if !discordID.Valid {
		return ""
	}
	return discordID.String
}

type roleState struct {
	userRoles map[string]bool
}

func mockDiscordAPI() http.HandlerFunc {
	state := &roleState{userRoles: make(map[string]bool)}
	
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(r.URL.Path, "/oauth2/token"):
			json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "token_type": "Bearer"})
		case strings.Contains(r.URL.Path, "/users/@me"):
			json.NewEncoder(w).Encode(map[string]any{"id": "discord-user-123"})
		case strings.Contains(r.URL.Path, "/members/") && r.Method == "GET":
			userID := extractUserIDFromPath(r.URL.Path)
			roles := []string{"other-role"}
			if state.userRoles[userID] {
				roles = append(roles, "role-id")
			}
			json.NewEncoder(w).Encode(map[string]any{"roles": roles})
		case strings.Contains(r.URL.Path, "/members/") && r.Method == "PUT":
			userID := extractUserIDFromPath(r.URL.Path)
			state.userRoles[userID] = true
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/members/") && r.Method == "DELETE":
			userID := extractUserIDFromPath(r.URL.Path)
			state.userRoles[userID] = false
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func extractUserIDFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "members" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

type mockTransport struct {
	server *httptest.Server
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := strings.Replace(req.URL.String(), "https://discord.com/api/v10", t.server.URL, 1)
	newURL = strings.Replace(newURL, "https://discord.com/api", t.server.URL, 1)
	newReq, _ := http.NewRequest(req.Method, newURL, req.Body)
	for k, v := range req.Header {
		newReq.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestDiscordIntegration(t *testing.T) {
	t.Run("adds member to role when payment is active", func(t *testing.T) {
		h := newTestHarness(t)
		defer h.close()

		h.createMember(1, "discord-user-123", "test@example.com", true)

		item, err := h.module.GetItem(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "1", item.MemberID)
		assert.Equal(t, "discord-user-123", item.DiscordUserID)
		assert.True(t, item.PaymentStatus.Valid)
		assert.Equal(t, "ActiveStripe", item.PaymentStatus.String)

		err = h.module.ProcessItem(context.Background(), item)
		assert.NoError(t, err)

		err = h.module.UpdateItem(context.Background(), item, true)
		assert.NoError(t, err)

		syncTime := h.memberSyncTime(1)
		assert.NotNil(t, syncTime)
		assert.InDelta(t, time.Now().Unix(), *syncTime, 5)
	})

	t.Run("removes member from role when payment is inactive", func(t *testing.T) {
		h := newTestHarness(t)
		defer h.close()

		_, err := h.db.Exec(`
			INSERT INTO members (id, discord_user_id, email, confirmed, waiver, fob_id)
			VALUES (?, ?, ?, 1, 1, ?)
		`, 2, "discord-user-456", "suspended@example.com", 200+2)
		require.NoError(t, err)

		item, err := h.module.GetItem(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "2", item.MemberID)
		assert.Equal(t, "discord-user-456", item.DiscordUserID)
		assert.False(t, item.PaymentStatus.Valid)

		err = h.module.ProcessItem(context.Background(), item)
		assert.NoError(t, err)

		err = h.module.UpdateItem(context.Background(), item, true)
		assert.NoError(t, err)

		syncTime := h.memberSyncTime(2)
		assert.NotNil(t, syncTime)
		assert.InDelta(t, time.Now().Unix(), *syncTime, 5)
	})

	t.Run("processes members in correct order", func(t *testing.T) {
		h := newTestHarness(t)
		defer h.close()

		h.createMember(3, "user-3", "user3@example.com", true)
		h.createMember(1, "user-1", "user1@example.com", true)
		h.createMember(2, "user-2", "user2@example.com", true)

		item1, _ := h.module.GetItem(context.Background())
		assert.Equal(t, "1", item1.MemberID)
		h.module.UpdateItem(context.Background(), item1, true)

		item2, _ := h.module.GetItem(context.Background())
		assert.Equal(t, "2", item2.MemberID)
		h.module.UpdateItem(context.Background(), item2, true)

		item3, _ := h.module.GetItem(context.Background())
		assert.Equal(t, "3", item3.MemberID)
	})

	t.Run("handles oauth callback flow", func(t *testing.T) {
		h := newTestHarness(t)
		defer h.close()

		h.module.authConf.Endpoint.TokenURL = h.server.URL + "/oauth2/token"
		h.createMember(123, "", "test@example.com", true)

		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, h.module.client.client)
		response := h.module.processDiscordCallback(ctx, 123, "auth-code")

		assert.NotNil(t, response)
		assert.Equal(t, "discord-user-123", h.memberDiscordID(123))
	})

	t.Run("schedules full reconciliation", func(t *testing.T) {
		h := newTestHarness(t)
		defer h.close()

		oldTime := time.Now().Unix() - 90000
		h.createMember(1, "user-1", "user1@example.com", true)
		h.createMember(2, "user-2", "user2@example.com", true)

		_, err := h.db.Exec("UPDATE members SET discord_last_synced = ? WHERE id = 2", oldTime)
		require.NoError(t, err)

		success := h.module.scheduleFullReconciliation(context.Background())
		assert.True(t, success)

		assert.Nil(t, h.memberSyncTime(1))
		assert.Nil(t, h.memberSyncTime(2))
	})
}

func TestDiscordBackoff(t *testing.T) {
	h := newTestHarness(t)
	defer h.close()

	h.createMember(1, "discord-user-123", "test@example.com", true)
	item := syncItem{MemberID: "1", DiscordUserID: "discord-user-123", PaymentStatus: sql.NullString{String: "ActiveStripe", Valid: true}}
	ctx := context.Background()

	t.Run("success updates sync time to now", func(t *testing.T) {
		err := h.module.UpdateItem(ctx, item, true)
		assert.NoError(t, err)

		syncTime := h.memberSyncTime(1)
		assert.NotNil(t, syncTime)
		assert.InDelta(t, time.Now().Unix(), *syncTime, 5)
	})

	t.Run("failure implements exponential backoff", func(t *testing.T) {
		_, err := h.db.Exec("UPDATE members SET discord_last_synced = NULL WHERE id = 1")
		require.NoError(t, err)

		err = h.module.UpdateItem(ctx, item, false)
		assert.NoError(t, err)

		syncTime := h.memberSyncTime(1)
		assert.NotNil(t, syncTime)
		expectedTime := time.Now().Unix() + 300
		assert.InDelta(t, expectedTime, *syncTime, 5)

		err = h.module.UpdateItem(ctx, item, false)
		assert.NoError(t, err)

		syncTime = h.memberSyncTime(1)
		expectedTime = time.Now().Unix() + 600
		assert.InDelta(t, expectedTime, *syncTime, 5)
	})

	t.Run("backoff is capped at one day", func(t *testing.T) {
		largeDelay := time.Now().Unix() + 50000
		_, err := h.db.Exec("UPDATE members SET discord_last_synced = ? WHERE id = 1", largeDelay)
		require.NoError(t, err)

		err = h.module.UpdateItem(ctx, item, false)
		assert.NoError(t, err)

		syncTime := h.memberSyncTime(1)
		expectedTime := time.Now().Unix() + 86400
		assert.InDelta(t, expectedTime, *syncTime, 5)
	})
}
