# BClouder billing runbook (invoice + timesheet)

Read this before any billing, invoice or timesheet task. The default report separates daily hours by **BClouder module/submodule**, with invoice time shown separately.

## Default reporting policy

Antitimely supplies timing evidence, separated under BClouder by **client: Rumo and VCNA**. Classify the actual work into modules/submodules **when generating the timesheet**, using session history, commits and test evidence. The report hierarchy is **client → module → submodule**. A client's stored bucket is a collection boundary; shared repository names do not establish its module allocation.

| Measure | Counting rule | Purpose |
|---|---|---|
| Invoice hours | Distinct company timestamps across its tracking buckets | Reconcile the numbered invoice; simultaneous company work is charged once. |
| Module/submodule hours | Distinct supported timestamps in each reporting category | Show effort by product area for BClouder's client allocation. |
| Total module hours | Sum of the nonduplicated leaf-category totals for the period | Show effort across concurrent work streams. |
| Overlapping module hours | Total module hours minus Invoice hours, after complete allocation | Explain parallel effort; it is not added again to the invoice. |

Show each module/submodule's daily hours separately. Do not show their sum as one person's daily worked hours. Ten hours on Master Data and ten hours on Packing Slips belong in separate columns; if they coincide completely, Invoice hours are ten, not twenty.

Antitimely still uses project records for these client buckets. The chosen configuration keeps Rumo and VCNA separate and removes module-level tracking buckets; the application schema and invoice calculation are unchanged. There is still no `atl timesheet` command. The client consolidation and historical backup are described below.

## Step 1 - Generate the invoice and retain its billing window

```bash
atl invoice list BClouder
atl invoice balance BClouder
atl invoice generate BClouder
```

- Only `invoice generate` renders a numbered PDF and closes the period. The menu's Send invoice and `invoice send` record a cutoff only.
- Flags must precede the company name. Capture explicit `--from`/`--to` bounds if supplied; otherwise the window runs from the previous non-advance anchor to generation time.
- Output: `~/Documents/Espanha/Autonomo/Invoices/ES-XXXX.pdf`.
- Preserve the number, exact bounds, tick interval, rate, subtotal, discount and credit applied. Compare hours to the Subtotal, not Amount Due: advance credit can make a correct invoice's Amount Due zero.
- An unnumbered row can be a legitimate historical cutoff. Investigate an accidental anchor; do not automatically delete unnumbered invoices.
- A transient `context deadline exceeded` can be retried. See `CLAUDE.md` for current daemon/Accessibility guidance; do not rebuild or change tracking data just to generate billing documents.

## Step 2 - Read exact boundaries and company membership

```bash
BILLING_DB="file:$HOME/.antitimely/db.sqlite?mode=ro"
sqlite3 -readonly "$BILLING_DB" "
SELECT i.number, i.sent_at, datetime(i.sent_at,'unixepoch','localtime')
FROM invoices i JOIN companies c ON c.id=i.company_id
WHERE c.name='BClouder' AND i.kind='hourly'
ORDER BY i.sent_at DESC, i.id DESC LIMIT 3;"

sqlite3 -readonly "$BILLING_DB" "
SELECT p.id,p.name,p.paused FROM projects p
JOIN companies c ON c.id=p.company_id WHERE c.name='BClouder';"
```

Use integer epoch bounds and the half-open interval `[START, END)`. Group days in Europe/Madrid, including daylight-saving changes. Do not round cutoffs to calendar dates. For the ordinary ES-0013 window, START was `1792071396` (Oct 15, 15:36:36 +02:00) and END was `1793621729` (Nov 2, 13:15:29 +01:00); these constants are historical examples, not defaults for another invoice.

Exclude advance invoices: they are anchor-neutral and can share the previous invoice's sent_at. Including one as an endpoint can collapse the period to zero. Retain explicit generation bounds when used; anchors alone cannot recover an overridden window.

Discover project/client membership dynamically. An old hard-coded `(5,6,7)` list omitted MD-Tracker and DED in ES-0011; after client consolidation those module bucket IDs no longer exist in the live DB. Legacy names such as Daas and MD-Tracker are historical allocation hints, not the reporting taxonomy. Verify the PDF's printed period against the actual bounds: an hourly cutoff during November 2 includes that morning even if the renderer prints November 1.

## Step 3 - Reconcile company time with the invoice

Set START and END to the actual epoch bounds from Step 2, then run:

```bash
sqlite3 -readonly "$BILLING_DB" "
SELECT COUNT(DISTINCT t.ts) AS tick_count,
       COUNT(DISTINCT t.ts)*5.0/3600 AS unrounded_invoice_hours
FROM ticks t JOIN projects p ON p.id=t.project_id
JOIN companies c ON c.id=p.company_id
WHERE c.name='BClouder' AND p.paused=0
  AND t.ts >= $START AND t.ts < $END;"
```

This mirrors `CountTicksForCompanyInRange` in `queries.sql`, including its current paused-project filter. Verify the configured tick interval and rate; the present BClouder setup uses five seconds and CAD 50/hour. `internal/invoice/lineitem.go` rounds hours to two decimals with round-to-even **before** multiplying by the rate. Pricing unrounded hours can disagree with the invoice.

Keep a private billing snapshot. Later project/company/pause changes can alter replay of the current query. A report reclassification must not silently change an issued invoice or add estimated recovered time. Advance behavior is documented in `superpowers/specs/2026-08-03-invoice-credit-drawdown-design.md`.

## Step 4 - Build a virtual module/submodule allocation

Retrieve the timestamped observations before aggregating. Retain timestamp, observation ID, source, cwd, session ID/window title, space ID and original project assignment in private working evidence. Read the live DB in read-only mode. Do not retag historical ticks just to produce a timesheet.

Start with these business labels, adding others only when supported by the period's work:

| Module | Submodule examples | Report labels |
|---|---|---|
| Packing Slips | General, Cargo Dockers, Auto Matcher | Packing Slips; PS - Cargo Dockers; PS - Auto Matcher |
| Deduction | General or a supported feature area | Deduction |
| Master Data | Cleanse, PDM, Vendor Requests, Public Credit Links | MD - Cleanse; MD - PDM; MD - Vendor Requests; MD - Public Credit Links |
| Shared | Shared infrastructure or unresolved allocation | Shared - Infrastructure; Shared - Unallocated |

These are a starting vocabulary, not a claim that every category has hours. Use stable names across periods. VCNA and Rumo are the client dimension, not substitutes for product modules. DaaS is a shared application/repository; its former tracking bucket was consolidated into VCNA, not relabelled Packing Slips. Use General only for the remainder attributable to a module when its narrower submodule is unknown.

Keep a private allocation record for each work interval: start/end or exact tick membership, client, module, submodule, source references, rationale, and verified/estimated/unresolved status. Retain the recorded client where supported and flag ambiguous client identity instead of moving time between Rumo and VCNA based on a module name. A long session can change modules: split it at evidenced task boundaries. Shared folders, commit counts, message counts and equal percentages do not establish a numerical split.

Counting rules:

1. Start with the company tick set reconciled in Step 3. Cover every invoiced tick with at least one supported leaf category; put the remainder in **Shared - Unallocated**. Clearly mark estimated/unresolved allocations. A partial allocation must not be presented as complete evidence for rebilling by module.
2. Within a leaf category, count a timestamp once regardless of how many sessions, agents, observations or worktrees report the same work. CPU and transcript signals for one task are not separate effort.
3. Let a timestamp occur in multiple leaf categories only when the evidence supports **distinct work running concurrently**. Duplicate logs, conflicting tracker labels, one change spanning two modules, or a coordinator and its worker do not by themselves establish extra parallel effort.
4. Total module hours is the sum of leaf totals. Overlapping module hours = Total module hours minus Invoice hours. Compute with exact counts before rounding. The difference measures additional concurrent module effort, not just the elapsed duration when any overlap occurred.
5. Deduplicate across children for any parent module/client rollup. Label parent rollups separately and never add parent and child totals together. Distinguish a client's elapsed union from its summed module effort when its modules run concurrently. Assigned child work must not also enter its parent's General remainder.
6. Preserve seconds/ticks internally and display two-decimal hours. Totals use unrounded records; include a short rounding note rather than changing hours to force displayed sums to match.

Legacy stored-project sums are useful diagnostics, not final module totals. In ES-0013, shared DaaS signals included Master Data activity. The `-By-Project` report is a **layout reference**, not a verified module allocation or a fixed total that later classification must reproduce. Correcting duplicate/misclassified project signals can change the summed effort without changing company time.

Put plausible untracked work in a separate recovery review. Source timestamps, elapsed test runs, stalled browser automation, handovers and automatic recaps alone do not establish new client hours. Product implementation, investigation, design and meaningful testing can support allocation when their timing is grounded. A recovery adjustment needs its own evidence and explicit acceptance before changing billing. Do not infer currency charges per module by pricing overlapping hours and claiming their sum equals the invoice.

## Step 5 - Verify assignments and descriptions against work evidence

This requires judgment; the CLI does not automate it. Check both sources below for each day/module. An investigation or design session need not produce a commit, but every description needs concrete evidence. Missing source evidence must not be replaced with a plausible story.

**Source A - git commits and changes.** Find the real repositories from observed workdirs, including worktrees. Common roots are:

- `~/focaApp/bclouder/daas/daas-back-end` and `daas/daas-front-end`, including their worktrees and `capex-sql/daas-back-end`.
- `~/focaApp/bclouder/vcna/vcna-invoice-scanner` and the invoice, packing-slip and reseller email-reader repos. Much VCNA work can be in the readers with no scanner commits.
- `~/focaApp/bclouder/rumo/rumo-mobile`.

```bash
git -C <repo> log --all --no-merges --since=<START_ISO> --until=<END_ISO> --date=iso-strict --pretty='%ct | %an <%ae> | %s'
```

Inspect all authors first, then describe only Rian's work. Commits span `jhionan@gmail.com` and `37809501+jhionan@users.noreply.github.com`; do not lose one identity or include teammates' output. ES-0006 nearly included 102 front-end commits by another author. Use commit times and exact epoch filtering at period edges. Commits show what changed, not how long it took; inspect actual commands and patches where work was uncommitted.

**Source B - coding sessions, tests and execution evidence.**

- Claude Code: `~/.claude/projects/*/*.jsonl`, including relevant worker evidence. Read substantive user/assistant messages and tool outcomes, not merely a file's newest timestamp.
- Codex: `~/.codex/sessions/` and `~/.codex/archived_sessions/`. Inspect the actual schema: messages can be `response_item` payloads with `role=user/assistant`, and final responses can use `phase=final_answer`. Looking only for `event_msg/user_message` or `phase=final` can silently miss real work. Include worker sessions without counting copied history or elapsed parent/child time twice.
- Opencode: its configured local session store, opened read-only. Check availability/schema before extraction. Missing historical sessions are an evidence gap, not proof that no work occurred.
- Saved test reports, browser screenshots and execution logs: distinguish completed assertions, partial work, skipped checks and tool failures. Describe designs as designed, tests as tested, and implementations as implemented.

Search command/work directories as well as starting cwd: a session can begin in a parent folder. Review every module on a multi-module day, without a whole-day prompt cap. The old seven-prompt cap hid VCNA deployment work in ES-0006. Automated review prompts, coordination chatter and system recaps alone do not establish output; concrete defects found and fixes/tests performed can.

**Source C - shell history** is corroborating only. Extended `~/.zsh_history` entries contain epoch times. They can confirm device testing or reveal work for another client, but do not write descriptions or allocate time from shell commands alone. Keep credentials and private customer data out of delivered reports.

Write module/submodule-prefixed, client-facing descriptions: `PS - Auto Matcher: added rule history and restore`, or `MD - Cleanse: validated classification results`. Keep meaningful investigation/testing and benefits. Omit handovers, agent/vendor attribution, coordination overhead, internal paths, branch names, migration numbers and class names. Group descriptions under the same categories as their hours. Fold evidenced manual meetings into their module/date. If no evidence supports the work, leave it unresolved rather than inventing an outcome.

## Step 6 - Generate the default CSV and PDF

Use the existing canonical filenames under `~/Documents/Espanha/Autonomo/Invoices/`:

- `BClouder-Timesheet-ES-XXXX.csv`
- `BClouder-Timesheet-ES-XXXX.pdf`

**CSV:** one row per day, one hours column for each supported leaf module/submodule, a separate Invoice hours column, and Work. Add a TOTAL row with exact period totals displayed to two decimals; leave its Work field empty. Example header:

```csv
Date,VCNA / PS - General hours,VCNA / PS - Cargo Dockers hours,VCNA / PS - Auto Matcher hours,VCNA / Deduction hours,VCNA / MD - Cleanse hours,Rumo / General hours,Shared - Unallocated hours,Invoice hours,Work
```

Include only relevant categories in stable client/module/submodule order. Group column headers by client in the PDF and retain the client prefix in CSV. Quote Work correctly, doubling embedded quotes. Do not add a combined daily Hours column. The old `Date,Hours,Work` report is legacy, not the default. If requested, add a long-form `Date,Client,Module,Submodule,Hours,Work` export for client allocation, with invoice reconciliation separate so unlike measures are not summed together.

**PDF:**

1. Daily module/submodule hours in separate columns, with Invoice hours visually distinct. If the taxonomy is too wide, use grouped module tables with daily submodule rows instead of shrinking the text.
2. Period totals per leaf category plus Total module hours, Overlapping module hours and Invoice hours. Explain once that parallel work can overlap and is charged once on this invoice.
3. Verified work descriptions by day and module/submodule. Keep daily entries together where practical and repeat table headers. Include the allocation basis, any unresolved shared bucket, and a short rounding note.

The ES-0013 `-By-Project` variant demonstrates the separated daily columns and invoice reconciliation. Future default reports should use evidence-based module labels, not automatically reuse its tracker labels or totals.

Render with the installed WeasyPrint:

```bash
weasyprint timesheet.html "$HOME/Documents/Espanha/Autonomo/Invoices/BClouder-Timesheet-ES-XXXX.pdf"
```

Render the PDF to images and inspect every page. The Invoices folder can allow full-path reads/writes even if directory listing fails because of macOS permissions. Preserve issued/historical files and back up current files before an authorized revision. Changing the report format does not authorize changing invoice amounts or live tracking state.

## Current tracking boundary: BClouder's clients

The chosen collection hierarchy is BClouder → **Rumo / VCNA**. Product modules such as Packing Slips, Cargo Dockers, Auto Matcher, Deduction and Master Data belong in virtual timesheet allocation, not separate collection buckets. This replaces the earlier idea of collapsing all BClouder work into a company-only bucket: client identity remains useful and must be retained.

On 2026-11-02 the live configuration consolidated Daas, MD-Tracker and DED into VCNA, retaining Rumo separately. Their tick assignments and rule targets were remapped without dropping tick rows, observations or matching conditions. All company time totals and invoice records were verified unchanged, including ES-0013's 131,139 ticks / 182.1375 unrounded invoice hours. No schema or application-code change was required.

A full pre-change SQLite backup and verification report preserve the original module/project assignments under `~/.antitimely/backups/bclouder-clients-apply-20261102T181744/` (`before.sqlite` and `verification.json`). The merge was rehearsed on a copy and the daemon restarted to reload the client mapping. Historical per-project sums will therefore differ in the live DB; issued files remain unchanged. Use the backup only as historical evidence, not as an automatic whole-DB restore that would discard newer ticks.

Keep the two client buckets and their matching rules. Do not recreate module buckets, merge/delete clients, retag history or change invoice anchors as part of timesheet generation. Preserve company/client identity, observation/session evidence, timestamps and concurrent streams, invoice cutoffs, advance credit, and reproducibility of issued invoices. One company/client daily total cannot reconstruct parallel work by itself. Virtual module allocations need their own evidence trail and revision history. Any future change to the project schema, client matching or pause/resume behavior needs a separate design.

## Final check before delivery

- No combined 30-hour day is presented as daily worked time; module/submodule hours are separate.
- Every invoiced timestamp has an allocation or Shared - Unallocated. Distinct concurrent work has evidence; duplicate signals and parent/child categories are not added twice.
- Invoice hours match the company union and invoice quantity/Subtotal after its rounding rule, not Amount Due. Before display rounding: Total module hours = Invoice hours + Overlapping module hours.
- Category totals and descriptions are reproducible. Estimated/unresolved allocation is visible; legacy project sums are not mistaken for verified module hours.
- CSV and PDF agree on dates, categories, hours and descriptions and share the invoice number. The PDF period matches the actual timestamp bounds.
- Client-facing descriptions state real work and outcomes; all rendered pages are readable.

## Current limitations

- `atl summary` can miss commits by looking at a container directory rather than its repos, and by filtering a single author email.
- There is no `atl timesheet` command; Steps 3-6 remain a manual evidence/allocation workflow. Future automation should keep timing separate from reviewable module classification.
- Slow tracker ticks can miss real work, while metadata events can extend transcript activity without proving it. Neither automatically establishes a billing correction. Use the current stable-signing and Accessibility guidance in `CLAUDE.md`.
