# Menu-driven invoice generation with company picker — design

**Date:** 2026-07-01
**Status:** approved (brainstorming) — ready for implementation plan

## Problem

Generating an invoice today requires the CLI (`atl invoice generate <Company>`), typing the exact, case-sensitive company name. The interactive menu (`atl` → Invoices) offers only *"Send invoice (record billing anchor)"*, which records a billing anchor but produces **no PDF** — a footgun that already created a numberless, PDF-less invoice and prematurely closed a billing period. The user works through the menu and wants to: pick a company from a list (no typing), generate the real PDF from the menu, sanity-check it before the invoice number is burned, and then have the PDF and its containing folder open automatically so the file can be handed to the client.

## Goals

- **Company selection by numbered list** wherever a company is chosen in the menu — no typing names.
- A menu path that **generates a real invoice PDF** (number + total + anchor), not just an anchor.
- A **preview + confirm** step (period + hours + amount) before the real generate, so the invoice number is never burned by accident.
- After a successful generate: **open the PDF** (Preview) and **reveal it in Finder** (so it can be dragged to the client).
- **Demote** the anchor-only "send" to a clearly-labeled advanced option — keep the escape hatch, remove the trap from the default path.

## Non-goals

- **Timesheet generation stays Claude-driven.** Per-day descriptions need the two-source (git commits + Claude Code console history) judgment a Go CLI can't do; this is documented in `docs/billing-runbook.md`. No timesheet tooling this iteration.
- No company-picker rollout to unrelated flows (project/company management) — only the invoice paths that currently type a name.
- No change to the invoice PDF rendering, billing math, or the `InvoiceGenerate` semantics themselves.

## Menu shape (after)

`atl` → `[N] Invoices`:

```
Invoices:
  [1] List all invoices
  [2] Generate invoice (PDF)          ← NEW (default path)
  [3] Delete invoice
  [4] Record anchor only (advanced)   ← demoted from old [2] "Send invoice"
  [b] Back
```

## Components

Each is small and independently testable.

| Unit | Responsibility | Depends on |
|---|---|---|
| `pickCompany(stdin) (name string, ok bool)` | Fetch companies via `CompanyList` RPC, print a numbered list, read a selection, return the chosen name. `ok=false` on blank / `b` / invalid. Reused by generate, delete-anchor(advanced), and any name-typing invoice path. | `CompanyList` RPC, `promptLine` |
| `formatInvoicePreview(reply) string` | Pure: turn an `InvoiceGenerateReply` (dry-run) into a one-line preview. **Hourly:** `"ES-0004 · 2026-06-16→2026-07-01 · 75.98h · CAD 3,799.00"`. **monthly_fixed** (e.g. Dentix): omit the hours segment → `"INV-014 · 2026-06-01→2026-06-30 · EUR 3,000.00"`. Mode is inferred from the reply (`Ticks==0` on a fixed invoice, or add a `BillingMode` field to the reply). | reply only |
| `invoiceGenerateFlow(stdin)` | Orchestrate: pick company → dry-run RPC → show preview → confirm y/N → real generate → open PDF + reveal folder. Retries on `context deadline exceeded`. | the above + `InvoiceGenerate` RPC + open helpers |
| `revealInFinder(path)` | macOS `open -R <path>` (reveals + selects the file in Finder). | `os/exec` |

## Small RPC enhancement (for a meaningful preview)

`InvoiceGenerateReply` currently returns `Number, PDFPath, TotalCents, Currency, IssueDateUnix, DueDateUnix` — but **not the resolved period, hours, or billing mode**. Add `FromUnix int64`, `ToUnix int64`, `Ticks int64`, and `BillingMode string` to the reply (the daemon already resolves the period, tick count, and mode while generating). This lets the preview show period + hours (hourly) or period + fixed amount (monthly_fixed). Without it the preview can only show `Number · CUR amount`; the period/mode make the check meaningful — so this is **in scope**.

## Data flow

```
menu [2] → pickCompany ──CompanyList RPC──▶ chosen name
        → invoiceGenerateFlow
             ├─ InvoiceGenerate{DryRun:true}  ──▶ preview (period, hours, amount)
             ├─ confirm y/N                     (N → back to menu, nothing burned)
             └─ InvoiceGenerate{DryRun:false} ──▶ real PDF
                   ├─ open <pdf>        (existing behavior in invoiceGenerate)
                   └─ open -R <pdf>     (NEW: reveal folder in Finder)
```

The real generate reuses the existing `invoiceGenerate` code path (which already runs `open <pdf>`); the flow adds only the reveal.

## Error handling

- Empty / invalid / `b` company selection → return to the Invoices menu, no side effects.
- **Dry-run or real generate returns `context deadline exceeded`** (daemon momentarily stalled — usually the `accessibility_denied` → hung-`osascript` chain): retry up to 3 times with a short pause; if still failing, print the error plus a one-line hint ("daemon busy — check Accessibility / try again") and return to the menu.
- Missing billing config / senders (RPC error text): surface it verbatim; don't swallow.
- The real generate burns the invoice number **only** on success; a dry-run never advances the cursor or writes a row.

## Testing

- `formatInvoicePreview` — pure, unit-tested against a constructed `InvoiceGenerateReply` (hourly amount, period formatting, zero-discount).
- `pickCompany` selection logic — split the parse/select core (list + raw input → index/name, or not-ok) from the I/O so it's unit-testable; test valid pick, out-of-range, blank, `b`.
- Menu I/O, `open`, and `open -R` are side-effecting glue and are not unit-tested, consistent with the rest of the CLI.

## Risks / edge cases

- **Double render** (dry-run + real) is ~2× the generate cost; acceptable, and the dry-run is what makes the preview safe.
- If the daemon is hard-down (not just stalled), the dry-run fails fast with "daemon unreachable" (exit-code-2 semantics) → surface and return to menu.
- `open -R` on a path in a TCC-restricted folder (`~/Documents/...`) still works (Finder reveal is user-initiated); only programmatic directory *listing* is restricted, not `open`.
