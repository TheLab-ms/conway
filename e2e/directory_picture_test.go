package e2e

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePNG returns a tiny valid PNG image as bytes.
func makePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// makeJPEG returns a tiny valid JPEG image as bytes.
func makeJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{0, 200, 0, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}))
	return buf.Bytes()
}

// buildPictureForm constructs a multipart/form-data body with the given file
// content and content-type, returning the body bytes and full content-type
// header (with boundary).
func buildPictureForm(t *testing.T, filename, contentType string, payload []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="picture"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := mw.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &body, mw.FormDataContentType()
}

func postPicture(t *testing.T, env *TestEnv, tok string, query string, body *bytes.Buffer, ct string) *http.Response {
	t.Helper()
	url := env.baseURL + "/directory/picture"
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequest("POST", url, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", ct)
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: "token", Value: tok})
	}
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	return resp
}

// TestDirectory_PicturePNG uploads a small valid PNG and verifies the member's
// profile_picture column is populated.
func TestDirectory_PicturePNG(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "png@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	body, ct := buildPictureForm(t, "pic.png", "image/png", makePNG(t))
	resp := postPicture(t, env, tok, "", body, ct)
	defer resp.Body.Close()
	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther,
		"unexpected status: %d", resp.StatusCode)

	var pic []byte
	require.NoError(t, env.db.QueryRow(
		`SELECT profile_picture FROM members WHERE id = ?`, memberID).Scan(&pic))
	assert.NotEmpty(t, pic, "profile_picture should be populated")
}

// TestDirectory_PictureJPEG uploads a small valid JPEG and verifies acceptance.
func TestDirectory_PictureJPEG(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "jpeg@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	body, ct := buildPictureForm(t, "pic.jpg", "image/jpeg", makeJPEG(t))
	resp := postPicture(t, env, tok, "", body, ct)
	defer resp.Body.Close()
	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther,
		"unexpected status: %d", resp.StatusCode)

	var pic []byte
	require.NoError(t, env.db.QueryRow(
		`SELECT profile_picture FROM members WHERE id = ?`, memberID).Scan(&pic))
	assert.NotEmpty(t, pic)
}

// TestDirectory_PictureGIFRejected verifies a GIF upload (unsupported content
// type) is rejected with a 4xx.
func TestDirectory_PictureGIFRejected(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "gif@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	// Minimal GIF89a header (not strictly a valid full GIF, but enough that
	// the server should reject by Content-Type before decoding).
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\xff\x00,")
	body, ct := buildPictureForm(t, "pic.gif", "image/gif", gif)
	resp := postPicture(t, env, tok, "", body, ct)
	defer resp.Body.Close()
	assert.GreaterOrEqual(t, resp.StatusCode, 400, "GIF should be rejected")
	assert.Less(t, resp.StatusCode, 500)
}

// TestDirectory_PictureTooLarge sends a 21MB body and verifies it is rejected.
// The handler installs http.MaxBytesReader at 20MB and ParseMultipartForm
// returns an error which is reported as 400.
func TestDirectory_PictureTooLarge(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "huge@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	huge := bytes.Repeat([]byte("A"), 21*1024*1024)
	body, ct := buildPictureForm(t, "pic.png", "image/png", huge)
	resp := postPicture(t, env, tok, "", body, ct)
	defer resp.Body.Close()
	// The directory handler currently returns 400 when MaxBytesReader trips.
	// Accept any 4xx as oversize-rejection.
	assert.GreaterOrEqual(t, resp.StatusCode, 400, "oversize body should be rejected")
	assert.Less(t, resp.StatusCode, 500)

	// Drain to free the connection.
	_, _ = io.Copy(io.Discard, resp.Body)

	var pic []byte
	require.NoError(t, env.db.QueryRow(
		`SELECT profile_picture FROM members WHERE id = ?`, memberID).Scan(&pic))
	assert.Empty(t, pic, "profile_picture should remain unset after rejection")
}

// TestDirectory_PictureDiscordOverride documents that the current /directory/picture
// handler does not support an explicit ?type=discord override; uploads always
// update profile_picture. We assert that behavior so the test fails loudly if
// the handler is changed.
func TestDirectory_PictureDiscordOverride(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "discordpic@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)

	body, ct := buildPictureForm(t, "pic.png", "image/png", makePNG(t))
	resp := postPicture(t, env, tok, "type=discord", body, ct)
	defer resp.Body.Close()
	require.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther,
		"unexpected status: %d", resp.StatusCode)

	var profile, discord []byte
	require.NoError(t, env.db.QueryRow(
		`SELECT profile_picture, discord_avatar FROM members WHERE id = ?`, memberID).Scan(&profile, &discord))
	// Caveat: the handler ignores ?type=discord today and always writes profile_picture.
	assert.NotEmpty(t, profile, "profile_picture is updated regardless of ?type=discord today")
	assert.Empty(t, discord, "discord_avatar is NOT touched by the picture endpoint today")
}

// TestDirectory_PictureRequiresAuth verifies that POST /directory/picture
// without a valid session redirects to /login.
func TestDirectory_PictureRequiresAuth(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	body, ct := buildPictureForm(t, "pic.png", "image/png", makePNG(t))
	resp := postPicture(t, env, "", "", body, ct)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.True(t, strings.Contains(resp.Header.Get("Location"), "/login"))
}
