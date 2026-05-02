# directory

Authenticated member directory: a grid of member cards (avatar, display name, pronouns, bio, leadership badge, Discord handle) plus a self-service profile editor.

## Routes

All routes require authentication.

- `GET /directory` - render the member grid
- `GET /directory/avatar/{id}` - serve avatar PNG; `?type=discord` forces the Discord avatar, otherwise profile picture is preferred with Discord avatar as fallback
- `POST /directory/picture` - multipart upload of profile picture
- `GET /directory/profile` - render edit form for current user
- `POST /directory/profile` - update pronouns, bio, and `directory_hidden` flag

## Schema

`New` lazily `ALTER TABLE`s the `members` table to add `profile_picture BLOB`, `bio TEXT`, `pronouns TEXT`, and `directory_hidden INTEGER DEFAULT 0`. Errors are ignored, which is how it tolerates the columns already existing. Avatars are stored inline in the DB as PNG blobs.

## Listing behavior (`queryMembers`)

A member appears in the directory only if all hold:
- `access_status = 'Ready'`
- `directory_hidden` is 0 / NULL
- `COALESCE(name_override, name)` is non-empty
- has a non-empty `profile_picture` OR `discord_avatar`

Display name uses `name_override` when set, otherwise the billing `name`.

## Ordering and privacy

Results are ordered by `fob_last_seen DESC`, then bucketed into 7-day windows and shuffled within each bucket (`shuffleWithinBuckets`). This preserves a rough recency signal without leaking who is currently at the space. The current user is then moved to the front of the list.

## Image processing (`image.go`)

- Upload limit: 20 MB (`MaxUploadSize`); enforced via `http.MaxBytesReader` and `ParseMultipartForm`
- Accepted content types: `image/jpeg`, `image/png` only
- `ProcessProfileImage` center-crops to a square, resizes to 160x160 (2x retina for the 96px display) using Catmull-Rom, and re-encodes as PNG
- Avatar responses set `Cache-Control: public, max-age=3600`

## Profile edit constraints

- `pronouns`: max 50 chars
- `bio`: max 100 chars
- Empty strings are stored as NULL
- After save: redirects to `/directory` if visible, or back to `/directory/profile` if `directory_hidden` was set (since the user wouldn't see themselves in the listing)

## Templates

`directory.templ` and `profile.templ` are templ-generated (`go generate` runs `templ generate`). The profile page includes a live-updating preview card driven by inline JS.
