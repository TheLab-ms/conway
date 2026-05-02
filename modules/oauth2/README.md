# oauth2

OIDC-ish identity provider for Conway. Exposes the standard endpoints used by downstream apps to authenticate members against Conway's user database.

## Endpoints

- `GET /oauth2/authorize` - requires Conway authn; mints a short-lived (1 minute) JWT "code" and redirects to `redirect_uri` with `code` and `state`.
- `POST /oauth2/token` - exchanges the code for an access/ID token. Uses HTTP Basic auth to identify the client; the `client_id` becomes the token audience.
- `GET /oauth2/userinfo` - returns `{id, name, email, groups}` for the bearer token's subject. Reads the member fresh from the DB on every call.
- `GET /oauth2/jwks` - publishes the RSA public key (single key, `kid=1`, RS256).
- `GET /.well-known/openid-configuration` - OIDC discovery document.

## Behavioral notes

- Authorization codes are stateless JWTs (audience `conway-oauth`, 1 min TTL) rather than DB rows.
- Access tokens are valid for 8 hours (`tokenValidity`) and signed RS256 by `engine.TokenIssuer`.
- The `redirect_uri` host must share a root domain (last two labels) with `self`. External redirects are rejected.
- The `conway` client_id is reserved; the token endpoint refuses to issue tokens with that audience. Any other client_id is accepted without registration, so issued oauth tokens should be treated as low-trust by consumers.
- `userinfo` derives the user's public ID by SHA-256 hashing the member's numeric ID and formatting the first 16 bytes as a UUID (cosmetic only).
- Group membership is computed live: `member` if `payment_status IS NOT NULL`, `admin` if `leadership` is set.
- The token's `sub` is the member's numeric DB ID at the token endpoint, but the authorization code's `sub` is the email (resolved to ID at exchange time, which also enforces that the member still exists).
