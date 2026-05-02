# google

Google OAuth2 login for Conway members.

## Routes

- `GET /login/google` — initiates OAuth2 flow. Accepts `callback_uri` query param, embedded in a signed JWT state token (5 min TTL, audience `google-login`).
- `GET /login/google/callback` — verifies state, exchanges code, fetches userinfo, looks up or creates the member by email.

## Configuration

Stored via `engine/config` under module key `google` (table `google_config`). Requires `client_id` and `client_secret` from Google Cloud Console. Redirect URI must be `<self>/login/google/callback`. Scopes requested: `openid`, `email`.

## Behavior

- `IsLoginEnabled` returns false unless both credentials are set and a `LoginCompleteFunc` has been registered.
- Emails are lowercased before lookup.
- If no member exists for the email:
  - If `SignupConfirmFunc` is registered, the confirmation page is rendered and the flow halts.
  - Otherwise (fallback) the member is created immediately via upsert and login proceeds.
- If the user denies consent or returns an `error` param, they are redirected to `/login` (302).
- Invalid/expired state returns 400; missing email on the Google account returns 400.
- HTTP client timeout is 10s; userinfo is fetched from `https://www.googleapis.com/oauth2/v2/userinfo`.

## Wiring

`SetLoginCompleter`, `SetSignupConfirm`, and `SetConfigLoader` must all be called before `AttachRoutes`. The module does not import the members module; account linkage is purely by email match against the `members` table.
