# Issue 219 Focused Transcript Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `Focused` and `Normal` transcript modes to the session view so long sessions can collapse down to the primary user/assistant exchange.

**Architecture:** Keep transcript mode separate from block visibility. Build display items as today, then run a pure focused-mode filter over those items, and finally render surviving items with the existing block-level visibility controls. Add a compact dropdown in the header and persist the selected mode in the UI store.

**Tech Stack:** Svelte 5, TypeScript, Vitest, `svelte-check`

---

## File Structure

- Create: `frontend/src/lib/utils/transcript-mode.ts`
  Purpose: pure transcript-mode filtering over `DisplayItem[]`
- Create: `frontend/src/lib/utils/transcript-mode.test.ts`
  Purpose: focused-mode behavior tests and edge cases
- Modify: `frontend/src/lib/stores/ui.svelte.ts`
  Purpose: transcript mode state, persistence, helpers
- Modify: `frontend/src/lib/stores/ui.test.ts`
  Purpose: transcript mode store tests
- Modify: `frontend/src/lib/components/content/MessageList.svelte`
  Purpose: apply transcript mode after display-item construction
- Modify: `frontend/src/lib/components/layout/AppHeader.svelte`
  Purpose: add the `Focused` / `Normal` UI control
- Modify: `frontend/src/App.svelte`
  Purpose: auto-switch to `Normal` when navigation targets a hidden ordinal

### Task 1: Transcript Mode Store

**Files:**
- Modify: `frontend/src/lib/stores/ui.svelte.ts`
- Modify: `frontend/src/lib/stores/ui.test.ts`

- [ ] **Step 1: Write failing store tests for transcript mode defaults and setters**

Add tests covering:
- default mode is `normal`
- setting mode to `focused` persists
- invalid local-storage values fall back to `normal`

- [ ] **Step 2: Run targeted store tests to verify failure**

Run: `npm test -- src/lib/stores/ui.test.ts`
Expected: new transcript-mode assertions fail

- [ ] **Step 3: Implement transcript mode state and persistence**

Add:
- `type TranscriptMode = "normal" | "focused"`
- local-storage key
- stored-value reader
- `setTranscriptMode()`

- [ ] **Step 4: Re-run targeted store tests**

Run: `npm test -- src/lib/stores/ui.test.ts`
Expected: PASS

### Task 2: Focused Transcript Filter

**Files:**
- Create: `frontend/src/lib/utils/transcript-mode.ts`
- Create: `frontend/src/lib/utils/transcript-mode.test.ts`

- [ ] **Step 1: Write failing pure tests for focused transcript behavior**

Cover:
- `user -> assistant -> tool -> assistant -> user`
- `user -> assistant -> tool -> user`
- `user -> tool -> assistant`
- `user -> tool`
- final assistant message at end of session
- tool-group items always hidden in `focused`

- [ ] **Step 2: Run the new transcript-mode tests and verify failure**

Run: `npm test -- src/lib/utils/transcript-mode.test.ts`
Expected: FAIL because the utility does not exist yet

- [ ] **Step 3: Implement the pure transcript-mode filter**

Create a utility that:
- returns original items for `normal`
- filters `DisplayItem[]` for `focused`
- keeps all user-message items
- hides all tool-group items
- keeps only the final assistant message in each assistant/tool stretch

- [ ] **Step 4: Re-run transcript-mode tests**

Run: `npm test -- src/lib/utils/transcript-mode.test.ts`
Expected: PASS

### Task 3: Wire Mode Into Message Rendering

**Files:**
- Modify: `frontend/src/lib/components/content/MessageList.svelte`
- Modify: `frontend/src/lib/components/layout/AppHeader.svelte`

- [ ] **Step 1: Add a focused/normal control in the header**

Expose the stored transcript mode with a compact dropdown/select near the
existing message controls.

- [ ] **Step 2: Apply transcript filtering in `MessageList`**

Build display items as today, then pass them through the transcript-mode
utility before sort/virtualization.

- [ ] **Step 3: Add or update tests if the UI wiring needs direct coverage**

Prefer keeping behavior covered by store + pure utility tests unless an
uncovered UI-specific regression appears.

- [ ] **Step 4: Run targeted tests covering affected utilities/stores**

Run:
`npm test -- src/lib/stores/ui.test.ts src/lib/utils/transcript-mode.test.ts`
Expected: PASS

### Task 4: Hidden-Target Navigation Recovery

**Files:**
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Add a failing test or minimal reproduction for hidden-target recovery**

If a direct component test is too heavy, extract a small helper and test that
instead. The important behavior is:
- when a requested ordinal is loaded but hidden by `focused`
- switch transcript mode to `normal`
- retry the existing scroll path

- [ ] **Step 2: Run the targeted test/reproduction and verify failure**

Run the smallest command that proves the hidden-target path currently does not
recover in `focused`.

- [ ] **Step 3: Implement the recovery**

Extend the pending-scroll logic so hidden ordinals trigger a mode switch to
`normal` before continuing.

- [ ] **Step 4: Re-run the targeted check**

Expected: PASS

### Task 5: Final Verification

**Files:**
- Modify: any of the files above as needed

- [ ] **Step 1: Run full frontend tests**

Run: `npm test`
Expected: PASS

- [ ] **Step 2: Run type checking**

Run: `npm run check`
Expected: `0 errors` (warnings may remain if unchanged)

- [ ] **Step 3: Review the diff for unintended behavior changes**

Run: `git diff --stat` and `git diff`
Expected: only transcript-mode files/changes are present

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/stores/ui.svelte.ts \
  frontend/src/lib/stores/ui.test.ts \
  frontend/src/lib/utils/transcript-mode.ts \
  frontend/src/lib/utils/transcript-mode.test.ts \
  frontend/src/lib/components/content/MessageList.svelte \
  frontend/src/lib/components/layout/AppHeader.svelte \
  frontend/src/App.svelte \
  docs/superpowers/specs/2026-03-23-issue-219-focused-transcript-mode-design.md \
  docs/superpowers/plans/2026-03-23-issue-219-focused-transcript-mode.md
git commit -m "feat: add focused transcript mode"
```
