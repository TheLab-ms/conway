package directory

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"io"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
)

// DirectoryMember represents a member in the directory listing.
type DirectoryMember struct {
	ID                int64
	DisplayName       string
	Bio               string
	DiscordUsername   string
	HasProfilePicture bool
	HasDiscordAvatar  bool
	Leadership        bool
	FobLastSeen       int64
}

// ProfileData represents the current user's profile for editing.
type ProfileData struct {
	ID                int64
	Name              string  // Original name from signup
	NameOverride      *string // Custom display name
	Bio               string
	HasProfilePicture bool
	HasDiscordAvatar  bool
	DiscordUsername   string
	Leadership        bool
}

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	// Add profile_picture column if it doesn't exist (ignore error if already exists)
	db.Exec(`ALTER TABLE members ADD COLUMN profile_picture BLOB`)
	// Add bio column if it doesn't exist
	db.Exec(`ALTER TABLE members ADD COLUMN bio TEXT`)
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /directory", router.WithAuthn(m.renderDirectoryView))
	router.HandleFunc("GET /directory/avatar/{id}", router.WithAuthn(m.serveAvatar))
	router.HandleFunc("POST /directory/picture", router.WithAuthn(m.handleProfilePictureUpload))
	router.HandleFunc("GET /directory/profile", router.WithAuthn(m.renderEditProfile))
	router.HandleFunc("POST /directory/profile", router.WithAuthn(m.handleEditProfile))
}

func (m *Module) renderDirectoryView(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserMeta(r.Context()).ID

	members, err := m.queryMembers(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	// Randomize within weekly buckets to prevent leaking who's currently there
	members = shuffleWithinBuckets(members)

	// Move current user to front of list
	for i, member := range members {
		if member.ID == userID {
			members = slices.Delete(members, i, i+1)
			members = slices.Insert(members, 0, member)
			break
		}
	}

	w.Header().Set("Content-Type", "text/html")
	renderDirectory(members, userID).Render(r.Context(), w)
}

func (m *Module) queryMembers(ctx context.Context) ([]DirectoryMember, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT
			id,
			COALESCE(name_override, name) as display_name,
			COALESCE(bio, '') as bio,
			COALESCE(discord_username, '') as discord_username,
			profile_picture IS NOT NULL AND LENGTH(profile_picture) > 0 as has_profile_picture,
			discord_avatar IS NOT NULL AND LENGTH(discord_avatar) > 0 as has_discord_avatar,
			leadership,
			COALESCE(fob_last_seen, 0) as fob_last_seen
		FROM members
		WHERE access_status = 'Ready'
			AND COALESCE(name_override, name) IS NOT NULL AND COALESCE(name_override, name) != ''
			AND (
				(profile_picture IS NOT NULL AND LENGTH(profile_picture) > 0)
				OR (discord_avatar IS NOT NULL AND LENGTH(discord_avatar) > 0)
			)
		ORDER BY fob_last_seen DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []DirectoryMember
	for rows.Next() {
		var m DirectoryMember
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.Bio, &m.DiscordUsername, &m.HasProfilePicture, &m.HasDiscordAvatar, &m.Leadership, &m.FobLastSeen); err != nil {
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

	// Check if a specific type is requested
	avatarType := r.URL.Query().Get("type")

	var avatar []byte
	if avatarType == "discord" {
		// Specifically request Discord avatar
		err = m.db.QueryRowContext(r.Context(),
			`SELECT discord_avatar FROM members WHERE id = ? AND discord_avatar IS NOT NULL`,
			id).Scan(&avatar)
	} else {
		// Default: prioritize profile_picture, fall back to discord_avatar
		err = m.db.QueryRowContext(r.Context(),
			`SELECT COALESCE(
				CASE WHEN profile_picture IS NOT NULL AND LENGTH(profile_picture) > 0
					 THEN profile_picture ELSE NULL END,
				CASE WHEN discord_avatar IS NOT NULL AND LENGTH(discord_avatar) > 0
					 THEN discord_avatar ELSE NULL END
			) FROM members WHERE id = ?`,
			id).Scan(&avatar)
	}

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

func (m *Module) handleProfilePictureUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize)

	if err := r.ParseMultipartForm(MaxUploadSize); err != nil {
		engine.ClientError(w, "Upload Error", "File too large. Maximum size is 20MB.", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("picture")
	if err != nil {
		engine.ClientError(w, "Upload Error", "No file provided.", http.StatusBadRequest)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if !ValidImageContentType(contentType) {
		engine.ClientError(w, "Invalid File", "Only JPEG and PNG images are allowed.", http.StatusBadRequest)
		return
	}

	imgBytes, err := io.ReadAll(file)
	if err != nil {
		engine.HandleError(w, err)
		return
	}

	processedImage, err := ProcessProfileImage(imgBytes)
	if err != nil {
		engine.ClientError(w, "Processing Error", "Could not process image. Please try a different file.", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserMeta(r.Context()).ID

	_, err = m.db.ExecContext(r.Context(),
		"UPDATE members SET profile_picture = $1 WHERE id = $2",
		processedImage, userID)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/directory/profile", http.StatusSeeOther)
}

func (m *Module) renderEditProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserMeta(r.Context()).ID

	profile, err := m.queryProfile(r.Context(), userID)
	if engine.HandleError(w, err) {
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderProfile(profile).Render(r.Context(), w)
}

func (m *Module) queryProfile(ctx context.Context, userID int64) (*ProfileData, error) {
	profile := &ProfileData{}
	err := m.db.QueryRowContext(ctx, `
		SELECT
			id,
			COALESCE(name, '') as name,
			name_override,
			COALESCE(bio, '') as bio,
			profile_picture IS NOT NULL AND LENGTH(profile_picture) > 0 as has_profile_picture,
			discord_avatar IS NOT NULL AND LENGTH(discord_avatar) > 0 as has_discord_avatar,
			COALESCE(discord_username, '') as discord_username,
			leadership
		FROM members
		WHERE id = ?`,
		userID).Scan(
		&profile.ID,
		&profile.Name,
		&profile.NameOverride,
		&profile.Bio,
		&profile.HasProfilePicture,
		&profile.HasDiscordAvatar,
		&profile.DiscordUsername,
		&profile.Leadership,
	)
	return profile, err
}

func (m *Module) handleEditProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserMeta(r.Context()).ID

	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Invalid Form", "Could not parse form data.", http.StatusBadRequest)
		return
	}

	bio := strings.TrimSpace(r.FormValue("bio"))

	// Validate bio length
	if len(bio) > 500 {
		engine.ClientError(w, "Bio Too Long", "Bio must be 500 characters or less.", http.StatusBadRequest)
		return
	}

	// Update profile bio only
	_, err := m.db.ExecContext(r.Context(), `
		UPDATE members SET
			bio = CASE WHEN $1 = '' THEN NULL ELSE $1 END
		WHERE id = $2`,
		bio, userID)

	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/directory", http.StatusSeeOther)
}
