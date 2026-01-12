# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Conway is makerspace management software for TheLab.ms. It handles membership billing (Stripe), door access controls (RFID/NFC via ESP32), passwordless email authentication, and integrations with Discord.

## Development Commands

```bash
make dev        # Run locally on http://localhost:8080 (generates templ, uses .dev/ for data)
make build      # Generate templates and build Linux binary
make seed       # Create a dev user with leadership access
make clean      # Remove .dev/ directory
```

Run a single test:
```bash
go test ./engine -run TestTokenIssuer
go test ./e2e -run TestLoginFlow  # E2E tests require Playwright (auto-installs)
```

The login flow prints the 5-digit code and login link to the console instead of sending email in dev mode.

To grant yourself leadership access:
```bash
sqlite3 .dev/conway.sqlite3 "UPDATE members SET leadership = true WHERE email = 'foo@bar.com'"
```

## Architecture

### Engine (`engine/`)
Core framework providing:
- **App/ProcMgr**: Process manager that runs modules with workers and HTTP routes
- **Router**: HTTP router wrapping stdlib mux with authentication middleware
- **TokenIssuer**: JWT signing/verification with auto-generated ED25519 keys
- **Database**: SQLite with WAL mode, single connection

### Modules (`modules/`)
Self-contained features implementing `AttachRoutes(*Router)` and/or `AttachWorkers(*ProcMgr)`. Registration order matters - auth and members modules must initialize first. Key modules:
- **auth**: Passwordless login via 5-digit codes, JWT sessions, Turnstile captcha
- **members**: Member schema and profile management
- **payment**: Stripe subscription handling
- **admin**: Leadership-only admin interface
- **kiosk**: On-site check-in terminal
- **fobapi**: API for access controllers to fetch authorized fob lists
- **machines**: Bambu 3D printer monitoring
- **discord/discordwebhook**: OAuth and notification integration

### Templating
Uses [templ](https://templ.guide) for type-safe HTML. Run `go generate ./modules/...` after editing `.templ` files. Generated files are `*_templ.go`.

### Access Controller (`access-controller/`)
MicroPython ESP32 firmware for door access. Reads 34-bit Wiegand credentials and authenticates against Conway's fob API. Separate from the main Go application.

## Key Patterns

- Modules call `engine.MustMigrate(db, migration)` in their constructor to apply schema
- Authentication: `router.WithAuthn(handler)` requires login, `router.WithLeadership(handler)` requires leadership role
- Config via environment variables with `CONWAY_` prefix (parsed with caarlos0/env)
- `engine.HandleError(w, err)` for consistent error handling in HTTP handlers
