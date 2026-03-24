# Issue 219 Focused Transcript Mode Design

## Goal

Add a transcript-mode control to the session view with `Normal` and
`Focused` modes, defaulting to `Normal`. `Focused` should reduce long
agent/tool-heavy sessions down to the key user/assistant exchange while
preserving the ability to fall back to the full transcript when needed.

## Scope

This design only covers:

- transcript mode state and persistence
- UI control for `Focused` / `Normal`
- focused transcript filtering semantics
- auto-switching back to `Normal` when navigation targets a hidden item

This design explicitly does not add a `Detailed` mode yet. The UI model
should leave room for it later without requiring another control redesign.

## User-Facing Behavior

### Modes

- `Normal`: current transcript behavior
- `Focused`: condensed behavior described below

The selected mode should persist in local storage and initialize to
`Normal` for users who have never chosen a mode.

### Focused Semantics

`Focused` operates on transcript display items, not raw content segments.

- Keep all non-system user messages.
- Hide all tool-group / tool-only display items.
- Between one user message and the next, keep only the final non-tool
  assistant message, if one exists.
- If the session ends on a non-tool assistant message, keep that final
  assistant message.
- If a user turn only leads to tool activity and no final assistant reply,
  show no assistant item for that stretch.

Examples:

- `user -> assistant -> tool -> assistant -> user`
  becomes `user -> assistant -> user`
- `user -> assistant -> tool -> user`
  becomes `user -> user`
- `user -> tool -> assistant`
  becomes `user -> assistant`
- `user -> tool`
  becomes `user`

### Interaction With Existing Block Filters

Transcript mode is a higher-level message filter. Existing block-visibility
filters remain available and continue to control content inside surviving
messages.

This separation is important:

- transcript mode decides whether a message/display item exists at all
- block filters decide which parts of a surviving message are shown

### Hidden-Target Navigation

If pinned-message navigation, command-palette navigation, or in-session
search targets an ordinal hidden by `Focused`, the app should automatically
switch to `Normal` before performing the scroll.

This mirrors the current “auto-enable thinking” behavior and prevents
navigation from silently failing in the condensed view.

## Implementation Shape

### State

Add a new `TranscriptMode` state to the UI store:

- values: `normal`, `focused`
- persistence key in local storage
- helpers for read/set

### UI

Add a compact transcript-mode dropdown in the session header near the
existing message controls.

It should:

- be visible only when a session is active
- show `Normal` and `Focused`
- default to `Normal`
- be implemented in a way that can later add `Detailed`

### Filtering Pipeline

Keep the current two-stage rendering pipeline, but make transcript mode a
distinct pass:

1. raw messages
2. system-message filtering
3. display-item construction (including tool grouping)
4. transcript-mode filtering
5. existing block-level visibility inside surviving messages

This keeps focused-mode logic out of content parsing and makes the behavior
easier to test as a pure transform.

## Files

Likely files to modify:

- `frontend/src/lib/stores/ui.svelte.ts`
- `frontend/src/lib/stores/ui.test.ts`
- `frontend/src/lib/components/layout/AppHeader.svelte`
- `frontend/src/lib/components/content/MessageList.svelte`
- `frontend/src/App.svelte`

Likely files to add:

- `frontend/src/lib/utils/transcript-mode.ts`
- `frontend/src/lib/utils/transcript-mode.test.ts`

## Testing Strategy

- add store tests for transcript-mode persistence/defaults
- add pure unit tests for focused transcript filtering examples and edge
  cases
- verify hidden-target navigation switches to `Normal` before scrolling
- re-run full frontend tests and `svelte-check`

## Risks

- mixing transcript mode and block filters in one layer would create
  confusing behavior
- implementing focused semantics on raw messages instead of display items
  would make tool-only grouping and end-of-turn handling harder to reason
  about
- hidden-target navigation must not silently fail when `Focused` is active
