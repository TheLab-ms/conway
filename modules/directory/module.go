package directory

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"math/rand"
	"net/http"
	"slices"
	"strconv"

	"github.com/TheLab-ms/conway/engine"
)

// DirectoryMember represents a member in the directory listing.
type DirectoryMember struct {
	ID              int64
	DisplayName     string
	DiscordUsername string
	HasAvatar       bool
	Leadership      bool
	FobLastSeen     int64
}

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /directory", router.WithAuthn(m.renderDirectoryView))
	router.HandleFunc("GET /directory/avatar/{id}", router.WithAuthn(m.serveAvatar))
}

func (m *Module) renderDirectoryView(w http.ResponseWriter, r *http.Request) {
	members, err := m.queryMembers(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	// Randomize within weekly buckets to prevent leaking who's currently there
	members = shuffleWithinBuckets(members)

	w.Header().Set("Content-Type", "text/html")
	renderDirectory(members).Render(r.Context(), w)
}

func (m *Module) queryMembers(ctx context.Context) ([]DirectoryMember, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT
			id,
			name as display_name,
			COALESCE(discord_username, '') as discord_username,
			discord_avatar IS NOT NULL AND LENGTH(discord_avatar) > 0 as has_avatar,
			leadership,
			COALESCE(fob_last_seen, 0) as fob_last_seen
		FROM members
		WHERE access_status = 'Ready'
			AND name IS NOT NULL AND name != ''
		ORDER BY fob_last_seen DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []DirectoryMember
	for rows.Next() {
		var m DirectoryMember
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.DiscordUsername, &m.HasAvatar, &m.Leadership, &m.FobLastSeen); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// shuffleWithinBuckets groups members by weekly buckets based on fob_last_seen
// and shuffles within each bucket to prevent leaking who arrived recently.
func shuffleWithinBuckets(members []DirectoryMember) []DirectoryMember {
	if len(members) == 0 {
		return members
	}

	const weekSeconds = 7 * 24 * 60 * 60

	// Group by week bucket
	buckets := make(map[int64][]DirectoryMember)
	var bucketKeys []int64

	for _, m := range members {
		bucket := m.FobLastSeen / weekSeconds
		if _, exists := buckets[bucket]; !exists {
			bucketKeys = append(bucketKeys, bucket)
		}
		buckets[bucket] = append(buckets[bucket], m)
	}

	// Sort bucket keys descending (most recent first)
	slices.SortFunc(bucketKeys, func(a, b int64) int {
		if b > a {
			return 1
		} else if b < a {
			return -1
		}
		return 0
	})

	// Shuffle within each bucket and concatenate
	var result []DirectoryMember
	for _, key := range bucketKeys {
		bucket := buckets[key]
		rand.Shuffle(len(bucket), func(i, j int) {
			bucket[i], bucket[j] = bucket[j], bucket[i]
		})
		result = append(result, bucket...)
	}

	return result
}

func (m *Module) serveAvatar(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var avatar []byte
	err = m.db.QueryRowContext(r.Context(),
		`SELECT discord_avatar FROM members WHERE id = ? AND discord_avatar IS NOT NULL`,
		id).Scan(&avatar)

	if err == sql.ErrNoRows || len(avatar) == 0 {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		engine.HandleError(w, err)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(avatar)
}
