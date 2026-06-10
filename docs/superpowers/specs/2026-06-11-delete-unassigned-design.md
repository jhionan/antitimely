# Delete unassigned ticks from `atl review`

**Date:** 2026-06-11
**Status:** Approved pending spec review

## Problem

`atl review` lists "unassigned signatures" — observations whose ticks have no
project (`project_id IS NULL`), grouped by observation, shown as "N ticks —
binary=… cwd=…". Per signature it offers: tag to a project, create a new
project, **ignore forever**, or skip.

There is no way to **delete** unassigned time. "Ignore forever"
(`ignored_observations`) only *suppresses future* ticks and hides the signature
from review — the past tick rows remain in the database and still exist as
billable-but-unassigned time. The user wants to remove chosen unassigned items
outright (noise/phantom ticks from one-off processes), picking which to delete.

## Decisions (from brainstorming)

- **Placement:** a new `[d] delete these ticks` action inside the existing
  `atl review` per-signature menu. Reuses the interactive selection loop; the
  user picks one signature at a time. (No standalone command, no bulk delete.)
- **Future handling:** **pure one-time delete.** Delete only removes the
  existing unassigned tick rows. It does NOT add an ignore rule, so if the
  process is still running the signature can reappear next poll. The user
  already has "ignore forever" for the suppress-future case; the two stay
  distinct.
- **Safety:** delete is destructive, so it requires a `[y/N]` confirmation
  showing the tick count before removing anything. (Contrast: `ignore` is not
  confirmed because it's non-destructive.)
- **Scope of deletion:** delete only the **unassigned** ticks for the selected
  observation (`project_id IS NULL AND observation_id = ?`). The `observations`
  row is left in place.

## Design

### CLI — new action (`internal/cli/review.go`)

In `handleOneSignature`, add `[d] delete these ticks` to the printed menu
(alongside `[n]`, `[i]`, `[s]`). Handle `case "d"`:

1. Print a confirmation that names what's being deleted and how much:
   `Delete <N> ticks for <describeSignature(sig)>? Permanently removes this time. [y/N]: `
   where `<N>` is `sig.Ticks`.
2. Read one line. If it is exactly `y` (case-insensitive), call the new RPC
   `DeleteSignature{ObservationID: sig.ObservationID}` and print
   `Deleted <reply.TicksDeleted> ticks.`. Any other input cancels (return to the
   loop without deleting).
3. Return `0` so the outer `cmdReview` loop refetches `PendingReview`; the
   deleted signature no longer appears (its ticks are gone, so the
   `JOIN ticks` in `PendingReviewSignatures` drops it).

The confirmation default is **No** (empty input cancels) — opposite of the
tag/ignore flows where empty often means "accept" — because this is destructive.

### RPC contract (`internal/rpcapi/api.go`)

```go
type DeleteSignatureArgs struct{ ObservationID int64 }
type DeleteSignatureReply struct{ TicksDeleted int64 }
```

### Daemon handler (`internal/daemon/rpc.go`)

`func (s *AntitimelyService) DeleteSignature(args rpcapi.DeleteSignatureArgs, reply *rpcapi.DeleteSignatureReply) error`

Calls a new query, sets `reply.TicksDeleted` from the affected row count, returns
any error. Single statement — no transaction needed.

### Query (`queries.sql`, regenerate sqlc)

```sql
-- name: DeleteUnassignedTicksForObservation :execrows
DELETE FROM ticks WHERE project_id IS NULL AND observation_id = ?;
```

`:execrows` returns the number of deleted rows for `TicksDeleted`.

## Why only unassigned ticks, and why keep the observation row

- The `project_id IS NULL` filter is a safety belt: if the observation also has
  ticks already assigned to a real project (e.g. a rule was added later), delete
  touches only the unassigned ones and can never erase billed project time.
- The `observations` row is left in place. `PendingReviewSignatures` and the
  unassigned-totals queries all `JOIN ticks`, so an observation with zero
  remaining ticks simply stops appearing and contributes nothing to any total.
  Deleting it would require handling the `ignored_observations` foreign key for
  no functional benefit (YAGNI).

## Error handling

- RPC/DB errors propagate to the CLI, which prints them to stderr (existing
  pattern in `review.go`); the review loop continues.
- A delete that matches zero rows (already gone / wrong id) returns
  `TicksDeleted: 0` and prints "Deleted 0 ticks." — harmless and truthful.

## Testing (`internal/daemon/rpc_test.go`)

1. **Happy path:** seed an observation with K unassigned ticks → `DeleteSignature`
   → assert `TicksDeleted == K`, those tick rows are gone, and
   `CountPendingReviewSignatures` drops by one.
2. **Safety — mixed observation:** an observation with both unassigned ticks and
   ticks assigned to a project → delete removes only the unassigned ones; the
   assigned ticks survive (assert their count unchanged).
3. **Isolation:** a second, untouched unassigned signature keeps all its ticks
   (no over-deletion).

## Out of scope (YAGNI)

- Bulk "delete all unassigned".
- A standalone `atl unassigned` command.
- Any suppress-future behavior on delete (that is what `ignore` is for).
- Deleting the `observations` row / orphan cleanup.
