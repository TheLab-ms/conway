# waiver

Renders and accepts liability waiver submissions.

## Routes

- `GET /waiver` — renders the latest waiver. Accepts `email` and `r` (redirect) query params to prefill the form and redirect after signing.
- `POST /waiver` — validates checkboxes, records the submission, and re-renders the page in a "signed" state. If `r` is set, JS redirects after 5s.

## Storage

Owns the `waiver_content` table (versioned markdown, autoincrement `version`, defaults seeded on migration). Submissions are written to a `waivers` table (`name`, `email`, `version`) which this module assumes exists but does NOT create — it must be provided by another module. Inserts use `ON CONFLICT DO NOTHING`, so a given (name, email, version) signs only once silently.

## Waiver markdown format

Parsed by `ParseWaiverMarkdown` (`markdown.go`):

- `# Title` — first occurrence sets the page title; subsequent `#` lines are ignored.
- `- [ ] Label` — required checkbox. All checkboxes must be checked on submit or the request fails with a 400 `Agreement Required` error.
- All other non-empty lines collapse into paragraphs separated by blank lines (newlines within a paragraph become spaces).

## Admin config

Exposes `ConfigSpec` backed by `waiver_content`, allowing admins to edit the markdown content. Saving creates a new version row; older versions are preserved and remain referenced by historical signatures.

## Behavioral notes

- The "signed" view pre-checks the boxes (driven by `name != ""`) so the user can print/screenshot a filled-out copy.
- `getLatestWaiverContent` always selects the highest `version`; there is no way to pin a user to an older version via the UI.
- No CSRF protection is implemented in this module.
