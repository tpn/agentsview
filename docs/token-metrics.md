# Token Metrics Design Note

## Problem Statement

AgentsView now surfaces token usage in both the session UI and the
analytics summary, but legacy stored data did not preserve an important
distinction: "the provider reported a token count of `0`" versus "the
provider never reported this metric at all."

Without explicit coverage flags, the product cannot tell whether a zero
value should participate in token analytics, whether a session badge
should show a real zero, or whether the UI should show a missing-data
placeholder instead. This especially affects `Output Tokens`, because
treating non-reporting sessions as zero-token sessions undercounts the
uncertainty and overstates the completeness of analytics.

## Goals

- Persist token coverage explicitly at both message and session level.
- Preserve the meaning of reported zero values.
- Exclude non-reporting sessions from token aggregates instead of
  silently treating them as zero.
- Repair legacy rows when existing stored signal is sufficient.
- Force a full resync when parser behavior changed in ways that make
  existing stored token semantics stale.
- Keep rollout non-destructive: preserve existing session history,
  orphaned sessions, and excluded-session metadata.

## Non-Goals

- Reconstruct token metrics that were never present in source data.
- Infer missing token coverage from zeros alone.
- Normalize all providers to emit identical token payload shapes.
- Retroactively make old unrecoverable rows distinguishable.
- Change unrelated analytics semantics outside token reporting.
- Provide a per-row PostgreSQL provenance model or a first-class
  convergence status surface in v1.

## User-Facing Semantics

### Output Tokens

`Output Tokens` in analytics is the sum of `sessions.total_output_tokens`
only for sessions where `sessions.has_total_output_tokens = true`.
Sessions that did not report output tokens do not contribute `0`; they
are excluded from the sum entirely.

This same exclusion rule applies to all `output_tokens` analytics:

- the summary total only includes reporting sessions
- the heatmap only accumulates output tokens from reporting sessions for
  each day
- `Top Sessions` with `metric=output_tokens` ranks only reporting
  sessions, ordered by `total_output_tokens DESC, id ASC`
- an empty state means "no reporting sessions in range", not "all
  sessions reported zero"
- newer clients should not show `output_tokens` heatmap/top-session
  controls unless the server exposes token-summary capability

### Reporting Sessions

`Reporting Sessions` is the count of sessions included in token-output
analytics. In practice, this is the number of sessions where output
token coverage is known, not the number of sessions in the filtered
result set overall.

On newer clients talking to older servers, `Output Tokens` and
`Reporting Sessions` may be absent entirely. In that case the UI should
render `—` rather than silently falling back to `0`, because the server
capability is unknown.

### `—` Placeholders

Session-level token badges use explicit placeholders when one side of
the pair is missing:

- `— ctx / 180 out` means output tokens were reported, context tokens
  were not.
- `2.4k ctx / — out` means context tokens were reported, output tokens
  were not.
- No badge is rendered when neither metric was reported.

The placeholder means "metric not reported," not zero.

### Visibility Rules

- Render a token badge only when at least one relevant metric is
  reportable.
- Use `—` only for a single missing side of an otherwise reportable
  token summary.
- Hide the badge entirely when neither side is reportable.
- Treat message/session badges, subagent badges, summary cards, heatmap,
  top sessions, and CSV export as different views over the same coverage
  semantics, not separate interpretations.

## Data Model Semantics

Coverage flags are presence markers, not non-zero markers:

- `messages.has_context_tokens` and `messages.has_output_tokens` mean
  the provider payload reported those message-level metrics, even when
  the numeric value was `0`.
- `sessions.has_peak_context_tokens` and
  `sessions.has_total_output_tokens` mean the session has authoritative
  evidence that those aggregates are reportable, either from parser-owned
  session aggregates or from recoverable message/session coverage during
  legacy repair.

Parser-owned message flags are authoritative. For older rows that
predate those flags, fallback inspection of `token_usage` keys is
best-effort only.

## Migration And Rollout Semantics

### Full Resync

SQLite `dataVersion` changes force a full resync when parser behavior
changed in a way that invalidates previously stored token semantics.
That path rebuilds a fresh database, reprocesses source files, copies
orphaned sessions and other preserved metadata, and atomically swaps the
rebuilt database into place.

Use full resync when:

- parser extraction rules changed
- aggregate token semantics changed
- previously stored rows cannot be trusted without reparsing source data

The token-metrics rollout deliberately uses both paths:

- parser/data-version changes force a full SQLite resync before the new
  parser semantics can be trusted for existing local archives
- current-schema databases that only lack explicit `has_*` flags can use
  one-time repair as a non-destructive bridge until or unless a full
  resync is required

### One-Time Repair

The one-time token coverage repair is for current-schema databases whose
stored rows are still usable but missing explicit token coverage flags.

- SQLite runs the repair once per database until a persisted repair
  marker is stored.
- PostgreSQL runs the repair when token coverage columns are first added,
  or when the repair marker is absent and the schema already contains
  sessions that may need backfill.

The repair only backfills flags; it does not invent new token counts.
It is not a substitute for full resync when parser semantics change.

### PostgreSQL Convergence

PostgreSQL does not reparse source files itself. Its convergence model is:

- schema upgrade adds token-coverage columns and can perform one-time
  repair on already-synced PG rows when enough stored signal exists
- if parser semantics changed, SQLite becomes the source of truth after
  local full resync
- PG only becomes equally trustworthy after a repaired/resynced local
  database pushes fresh session/message rows again
- until that push happens, PG-backed views may remain best-effort for
  older rows even though the schema is upgraded

Operationally, PG-backed deployments should assume:

- `pg serve` is fully trustworthy for newly pushed or newly repaired PG
  rows
- historical PG rows produced under older parser semantics may lag until
  a local client republishes them
- the recovery path is `pg push --full` from a machine whose SQLite DB
  has already completed the required repair or full resync
- v1 does not provide a measurable per-row convergence/provenance marker;
  operators must treat PG token analytics as operationally best-effort
  until the relevant repaired/resynced SQLite sources have republished
  them

### Best-Effort Only

Legacy repair is intentionally limited to rows with enough surviving
signal to infer coverage:

- non-zero `context_tokens` or `output_tokens`
- `token_usage` payloads that still contain token keys, including
  explicit zero-valued keys such as `{"output_tokens":0}`
- session aggregates that already stored non-zero totals or peaks

If a legacy row has empty `token_usage`, zero numeric counts, and no
stored coverage flags, there is no reliable way to distinguish "not
reported" from "reported zero." Those rows remain unrecoverable without
reparsing source files, and even a full resync only helps if the source
files still preserve the original token signal.

## Provider Differences

Providers do not report token usage uniformly:

- Some providers emit explicit per-message token keys, including keys
  whose value is `0`. These are recoverable because presence is visible.
- Some providers only contribute session-level aggregates or uneven
  message/session coverage, so session flags may be known even when one
  message field is absent.
- Older ingested rows may predate parser-owned coverage flags entirely,
  leaving only raw numeric values or partial `token_usage` blobs.

The system therefore treats token coverage as provider-specific metadata
that must be preserved, not re-derived from a single universal rule.

## Acceptance Criteria

- Reported zero token values survive end-to-end without being mistaken
  for missing data.
- Analytics `Output Tokens` excludes sessions with unknown output-token
  coverage.
- Analytics `Reporting Sessions` matches the count of sessions included
  in token-output totals.
- Session token badges show `—` only for genuinely missing metrics.
- SQLite and PostgreSQL both perform one-time coverage repair when
  appropriate, and skip it once the persisted marker is present.
- Parser data-version bumps require a full resync instead of silently
  trusting stale token semantics.
- Unrecoverable legacy rows remain excluded rather than being
  misclassified.
- SQLite instances with stale parser semantics clearly enter
  `NeedsResync()` / resync-required state.
- One-time repair emits enough logging/state for operators to tell when
  it ran and whether it updated anything.
- PG-backed deployments have an explicit documented expectation that
  repaired/resynced local data must be pushed before PG analytics are
  fully converged.

## Compatibility Expectations

### SQLite

- Existing SQLite databases may open in one of two modes:
  - repair-only, when stored token data is still semantically valid but
    explicit `has_*` flags are missing
  - full-resync-required, when `dataVersion` indicates parser semantics
    changed and existing stored token meaning may be stale
- During the repair-only path, historical rows may be best-effort until
  the one-time repair completes.

### PostgreSQL

- Existing PostgreSQL schemas accept additive `has_*` columns and a
  one-time repair step without dropping stored session history.
- `pg serve` and SQLite-backed `serve` are expected to return the same
  token fields and analytics metrics for the same repaired data set.
- If PG rows were originally pushed from a stale local parser build,
  schema repair alone may not fully converge them; a subsequent push from
  a repaired/resynced SQLite source is the compatibility bridge.

### API And CSV

- New API fields and metric enums are additive.
- Older clients may ignore the new fields safely.
- Older servers may omit `total_output_tokens` and
  `token_reporting_sessions` entirely.
- Newer clients must assume mixed-version data can exist briefly during
  rollout and therefore treat unknown coverage as unknown, not zero.
- UI surfaces should render `—` for missing summary capability rather
  than `0`.
- Heatmap and top-session `output_tokens` controls should remain hidden
  when the server does not expose the token-summary capability needed to
  back them.
- CSV export mirrors the new summary semantics: `Output Tokens` only
  counts reporting sessions, and `Reporting Sessions` makes that
  coverage explicit.

## Rollout Order

1. Define the token coverage model and compatibility rules.
   Gate: written semantics for `has_*`, `Output Tokens`, `Reporting Sessions`,
   placeholders, and unrecoverable legacy rows.
2. Land SQLite schema changes plus one-time repair markers.
   Gate: legacy SQLite DBs can open, repair once, and stop rescanning.
3. Land PostgreSQL schema changes plus one-time repair markers.
   Gate: upgraded PG schema can repair once and preserve token fields.
4. Land parser-owned token presence for supported providers.
   Gate: explicit zero-valued token keys keep `Has*Tokens=true`.
5. Require full SQLite resync when parser semantics change.
   Gate: stale local DBs surface resync-required state before user-facing
   analytics are trusted or stale token semantics can propagate.
6. Land sync/push/upload propagation.
   Gate: full sync, incremental append, upload, and PG push preserve
   token coverage truth, and local databases in resync-required state
   are not treated as trusted token sources.
7. Land analytics endpoints/store/API semantics.
   Gate: SQLite and PG analytics agree for repaired/resynced data, and
   `output_tokens` excludes non-reporting sessions.
8. Land UI surfacing.
   Gate: badges, controls, placeholders, and summary cards all match the
   documented semantics on desktop and mobile.
9. Run final integrated verification and history cleanup.

This order matters because user-visible analytics and UI must not be the
first consumer of stale token semantics.

## Verification Matrix

- Legacy SQLite DB with missing `has_*` flags but recoverable token
  signal
- Legacy current-schema SQLite DB that needs one-time repair exactly
  once
- SQLite DB that requires full resync because parser semantics changed
- PostgreSQL schema upgrade with one-time repair marker
- repaired/resynced SQLite -> `pg push --full` -> PG parity
- Incremental append sync after repair and after resync-required state
- Upload path for sessions with explicit zero-valued token keys
- Analytics summary, heatmap, top-sessions, and CSV export for reporting
  vs non-reporting sessions
- Frontend badges and placeholders for reported, partially reported, and
  missing token data

## Operational Notes

- One-time repair is expected to scan only candidate rows and stop once
  the repair marker is recorded.
- If repair markers are missing or a repair is interrupted, reopen will
  rerun repair until the marker is persisted.
- If repair output is incorrect because parser semantics changed, the
  recovery path is a full SQLite resync, not repeated best-effort
  repair.
- Expected repair cost is proportional to candidate legacy rows. On a
  large archive this may be noticeable once, but it should not recur on
  every startup after the marker is set.
- Operators should expect log evidence for:
  - whether one-time repair ran
  - how many message/session rows were updated
  - whether the local SQLite DB is in resync-required state
  - whether PG still needs a fresh push from a repaired/resynced source

## Rollback And Containment

- If PG analytics remain misleading after schema upgrade, contain the
  issue by running `pg push --full` from a repaired/resynced local DB
  before relying on PG-backed dashboards.
- v1 does not expose a first-class PG convergence status surface. The
  supported operational stance is therefore conservative: treat PG token
  analytics as best-effort until the relevant repaired/resynced local
  sources have republished them, and do not promise a stronger guarantee
  in UI or API status output.
- If SQLite repair produced insufficient coverage because the source
  files are still available, prefer full resync over repeated repair.
- If source files are gone and legacy rows are unrecoverable, the system
  should continue to exclude them from reporting-based analytics rather
  than inventing token coverage.
