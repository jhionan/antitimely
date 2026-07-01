# BClouder monthly billing runbook (invoice + timesheet)

How to produce the **invoice** and the **timesheet** each month. Read this first — the two documents use **different hour counts on purpose**.

## The core rule: deduped vs all-hours

| Document | Hours | Meaning | Command |
|---|---|---|---|
| **Invoice** | **Deduped** — `COUNT(DISTINCT ts)` across the company's projects | Work on two projects for the same company **in the same second is charged once**. Never double-bill. | `atl invoice generate` |
| **Timesheet** | **All hours worked** — sum of each project's hours | Concurrent work is counted **per project**, so the total is **≥ the invoice**. Shows the client everything worked. | hand-assembled (see below) |

The timesheet total is **always ≥** the invoice total. The gap is concurrent multi-project time. (Example: ES-0004 → timesheet **82.66 h**, invoice **75.98 h**, gap 6.68 h.)

---

## Step 1 — Generate the invoice with `atl` (this sets the period)

⚠️ **Do NOT use the menu** (`atl > N > 2` "Send invoice"): it only records a billing *anchor* and produces **no PDF**. Use the CLI:

```bash
# If a stray anchor-only row exists (from the menu), delete it first:
atl invoice list BClouder            # find the id of any row with no number/total
atl invoice delete <id>

# Real generate — writes the PDF, number, total; advances the number; sets the anchor:
atl invoice generate BClouder        # flags (if any) BEFORE the company name; name is case-sensitive
```

- Output PDF: `~/Documents/Espanha/Autonomo/Invoices/ES-XXXX.pdf`
- If it fails with **`context deadline exceeded`**, the daemon is momentarily stalled (usually `accessibility_denied` → hung `osascript`). Just retry a few times; it lands in a good window. Fixing accessibility removes the stalls.
- Note the **number** (ES-XXXX), the **total** (CAD), and the **anchor time** it set — you need them below.

## Step 2 — Get the exact period boundaries

```bash
DB="file:$HOME/.antitimely/db.sqlite?mode=ro"
# The two most recent BClouder anchors: the newest = END (this invoice), the one before = START.
sqlite3 -readonly "$DB" "SELECT number, datetime(sent_at,'unixepoch','localtime') FROM invoices i JOIN companies c ON c.id=i.company_id WHERE c.name='BClouder' ORDER BY sent_at DESC LIMIT 3;"
START=$(date -d "<previous anchor, e.g. 2026-06-16 15:52:00>" +%s)
END=$(date -d "<this invoice anchor, e.g. 2026-07-01 18:11:17>" +%s)
```

BClouder project ids: **VCNA=5, Rumo=6, Daas=7** (verify: `SELECT id,name FROM projects p JOIN companies c ON c.id=p.company_id WHERE c.name='BClouder';`).

## Step 3 — Reconcile the invoice (deduped) — sanity check

```bash
sqlite3 -readonly "$DB" "SELECT printf('deduped: %.2f h -> CAD %.2f', COUNT(DISTINCT ts)*5.0/3600, COUNT(DISTINCT ts)*5.0/3600*50) FROM ticks WHERE project_id IN (5,6,7) AND ts>=$START AND ts<$END;"
```
This should match the invoice total (rate is CAD 50.00/h; modulo banker's-rounding cents).

## Step 4 — Compute TIMESHEET hours (all hours, per day)

Per-day = **sum of each project's distinct-second count** (NOT deduped):

```bash
# Per-day all-hours:
sqlite3 -readonly "$DB" "
SELECT day, printf('%.2f', SUM(cnt)*5.0/3600) FROM (
  SELECT date(ts,'unixepoch','localtime') day, project_id, COUNT(DISTINCT ts) cnt
  FROM ticks WHERE project_id IN (5,6,7) AND ts>=$START AND ts<$END
  GROUP BY day, project_id
) GROUP BY day HAVING SUM(cnt)>6 ORDER BY day;"     -- HAVING drops negligible <30s blips

# Grand total (this is the timesheet total, >= invoice):
sqlite3 -readonly "$DB" "SELECT printf('%.2f h', SUM(cnt)*5.0/3600) FROM (SELECT COUNT(DISTINCT ts) cnt FROM ticks WHERE project_id IN (5,6,7) AND ts>=$START AND ts<$END GROUP BY project_id);"
```

## Step 5 — Gather descriptions from git commits

`atl summary` is the intended tool BUT is **currently broken** for this (see Known issues) — until fixed, gather commits manually. The BClouder repos:

- `~/focaApp/bclouder/daas/daas-back-end` (.NET) and `~/focaApp/bclouder/daas/daas-front-end` (Angular) — Daas
- `~/focaApp/bclouder/capex-sql/daas-back-end` — Daas worktree (capex branch)
- `~/focaApp/bclouder/vcna/vcna-invoice-scanner` — VCNA
- `~/focaApp/bclouder/rumo/rumo-mobile` — Rumo

```bash
git -C <repo> log --all --since=<START date> --until=<END date> --date=format:'%Y-%m-%d' --pretty='%cd | %s'
```
Include **all authors' worktree branches** (your commits use both `jhionan@gmail.com` and `37809501+jhionan@users.noreply.github.com`).

**Write the Work column in client-facing prose** (see ES-0002/ES-0003 for tone): product-prefixed (`DaaS —`, `Rumo —`, `VCNA —`), benefit/feature-oriented, **no internal jargon** (no migration numbers, class names, branch names, cwd paths). One line per day. Fold any manual entries (e.g. meetings logged via tick backfill) into that day's note.

## Step 6 — Write the two files (CSV + PDF) into the Invoices folder

Location + naming (match the existing set): `~/Documents/Espanha/Autonomo/Invoices/BClouder-Timesheet-ES-XXXX.csv` and `...ES-XXXX.pdf`.

**CSV** format is `Date,Hours,Work` with a final `TOTAL,<all-hours>,` row; decimal hours (2 dp); Work quoted, `"` inside escaped as `""`.

**PDF**: render an HTML table with **weasyprint** (installed):
```bash
weasyprint timesheet.html "$HOME/Documents/Espanha/Autonomo/Invoices/BClouder-Timesheet-ES-XXXX.pdf"
```
(The Invoices folder is writable even though `ls` on it may report "Operation not permitted" — that's a macOS TCC quirk on directory metadata; file writes/reads by full path work.)

## Final check before sending
- Timesheet total (all-hours) **≥** invoice total (deduped). If it's not, something's wrong.
- The invoice PDF (ES-XXXX.pdf) and both timesheet files share the same ES-XXXX number.
- Descriptions read like a client would want, not like git.

---

## Known issues to fix (make this one-command later)
1. **`atl summary`** finds no commits: it runs git at the *container* dir (`~/focaApp/bclouder/daas/`, not a repo) instead of walking to the real sub-repos; and it filters a single git-config email while your commits span multiple identities.
2. **No `atl timesheet` command** — Steps 3–6 should become one command that emits the CSV+PDF with all-hours + fed descriptions.
3. **Accessibility grant resets on every `make rebuild`** (cdhash changes) → `accessibility_denied` → daemon stalls → invoice `context deadline exceeded`. Durable fix: ad-hoc codesign the binary in the build.
