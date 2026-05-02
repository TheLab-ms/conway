# metrics

Stores time-series metric samples and exposes a configurable dashboard for charting them.

## Schema

- `metrics` — append-only `(timestamp, series, value)` samples. Indexed on `(series, timestamp)`. Timestamps are unix seconds with subsecond precision.
- `metrics_samplings` — registry of named sampling jobs (query, interval, target table). Defined here but populated by other modules.
- `metrics_config` — versioned chart configuration as JSON. New rows are inserted rather than updated; the highest `version` wins.

## Behavior

- On startup, `New` runs migrations and calls `seedDefaults`, which inserts a default chart config row only if `metrics_config` is empty. Defaults cover `active-members`, `daily-unique-visitors`, `weekly-unique-visitors`, and `monthly-unique-visitors`. There is no auto-discovery fallback — series without a configured chart are not displayed.
- `AttachWorkers` registers a daily cleanup that deletes samples older than `defaultTTL` (2 years). TTL is a compile-time constant, not configurable.
- This module does not write samples itself; producers insert directly into the `metrics` table using their own series names.

## Configuration

`Config.Charts` is exposed via the standard config system (order 50). Each `ChartConfig` requires `Title` and `Series`; `Color` is an optional hex string (defaults to teal in the UI). The `Series` value must match a series name present in the `metrics` table for the chart to render data.
