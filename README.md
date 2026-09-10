# Conway

The makerspace management software used by TheLab.ms in Richardson, TX.

## Cloudflare Workers

The membership-core reimplementation lives in [`workers/`](workers/README.md):
TypeScript, D1, Queues, Durable Objects, and a zero-build native web-components
frontend. It uses Discord-only authentication and has no member images or email
delivery. See the [migration runbook](workers/MIGRATION.md) before moving existing
data, and [`conwayedge/`](conwayedge/README.md) for the LAN/controller companion.
The Go application and its deployment instructions below remain intact for the
existing deployment and controlled migration rollback.


## What does it do?

Tons of stuff! Billing for monthly membership dues (with Stripe and kind of PayPal for legacy reasons), door access controls, etc.


## That's cool! Can I use it for my makerspace?

That should in theory be possible. Open a GH issue if you're serious and we'll write up some docs to help you through the setup process.


## Development

Install Go 1.24(ish), then just `make dev` and browse to http://localhost:8080.
The login flow will print the 5-digit code and login link to the console instead of actually sending an email.

Run `make seed` to insert a leadership account for `dev@localhost`.

### Deployment

See: https://github.com/TheLab-ms/infra
