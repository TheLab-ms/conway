# bootstrap

Provides the base HTML document shell used by every page in Conway. Renders
`<!DOCTYPE html>` through `<body><main>{children}</main></body>` and includes
the standard asset bundle: Bootstrap 5 CSS/JS, htmx, Chart.js (+ date-fns
adapter), `conway.css`, and the Space Grotesk / Source Code Pro web fonts.

## Components

- `View()` — wraps children in the standard document shell.
- `DarkmodeView()` — currently identical to `View()`. Both render the
  document with `data-bs-theme="dark"` hard-coded on `<html>`; there is no
  light-mode variant despite the name.

## Behavioral notes

- Title is hard-coded to `TheLab Makerspace`.
- Theme color meta is `#00C853`.
- All asset paths are absolute (`/assets/...`) and must be served by the host
  application.
- Scripts in `<head>` are loaded synchronously (no `defer`/`async`).
- `bootstrap_templ.go` is generated from `bootstrap.templ` via
  `go generate` (see `module.go`); do not edit it by hand.
