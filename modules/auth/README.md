# auth

Passwordless authentication module. Issues session cookies via JWT after verifying a 5-digit email code, and provides middleware to gate HTTP handlers.

## Routes

- `GET /login` - login form (email + optional Discord/Google buttons).
- `POST /login` - starts an email login. If no member exists for the email, renders the signup confirmation page instead of sending a code.
- `GET /login/sent` - "check your email" page with a 5-digit code form.
- `POST /login/code` - submits a code from the form.
- `GET /login/code?code=` - one-click login from the email link.
- `POST /login/confirm-signup` - creates a new member from a signed `signup-confirm` token, then completes login per provider (`email`, `discord:<user_id>`, `google`).
- `GET /logout` - clears the `token` cookie and redirects to `callback_uri`.
- `GET /whoami` - JSON dump of `UserMetadata` for the current session.

## Middleware

- `WithAuthn` - requires a valid `token` cookie whose JWT audience is `conway` and whose subject resolves to a row in `members`. On failure, redirects to `/login?callback_uri=<current URL>`. Injects `*UserMetadata` into the request context (retrieve via `GetUserMeta`).
- `WithLeadership` - wraps `WithAuthn` and returns 403 unless `members.leadership` is true.
- `OnlyLAN` - returns 403 if `CF-Connecting-IP` is set (i.e. request came through Cloudflare).

## Behavioral details

- Login codes are stored in the `login_codes` table, are single-use (deleted on consumption or expiration), and expire after 5 minutes. A background worker (`AttachWorkers`) sweeps expired rows hourly.
- Code generation retries up to 3 times on PRIMARY KEY collision, then errors.
- Code submission is rate-limited globally to 5/sec via a shared `rate.Limiter` (the limiter blocks rather than rejects).
- The session JWT has audience `conway`, subject `<member_id>`, and a 30-day expiry. The cookie is `SameSite=Lax`, `Path=/`, and `Secure` only when `self.Scheme` contains "s" (https).
- On successful login, `members.confirmed` is flipped to true if it was false.
- Signup is gated by a separate JWT with audience `signup-confirm` and a 10-minute expiry. The `Issuer` claim encodes the originating provider, so the same confirmation flow works for email, Discord (`discord:<user_id>` - links Discord account on creation), and Google.
- Cloudflare Turnstile verification is **fail-open**: if `turnstile` is nil, the network call fails, or Cloudflare returns >=400, the request is allowed through. Only an explicit `success: false` response blocks login.
- `DiscordLoginEnabled` / `GoogleLoginEnabled` are nil by default; the discord/google modules set them at wire-time. If nil, the corresponding button is hidden.
- `CompleteLoginForMember` is the entry point external OAuth modules use to finalize a session; it mints a short-lived internal JWT and feeds it through the normal `completeLogin` path.
- New members are created via `INSERT ... ON CONFLICT(email) DO UPDATE SET email=email RETURNING id`, so concurrent signup confirmations for the same email are safe.
