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

## Step 5 — Gather + verify descriptions from TWO sources (Claude-driven, not automatable yet)

This step stays a **Claude task** — it needs judgment that the CLI can't do. Do NOT trust a single source; every day's description must be grounded in **both** of the following, cross-checked:

**Source A — git commits** (what shipped). The BClouder repos:

- `~/focaApp/bclouder/daas/daas-back-end` (.NET) and `~/focaApp/bclouder/daas/daas-front-end` (Angular) — Daas
- `~/focaApp/bclouder/capex-sql/daas-back-end` — Daas worktree (capex branch)
- `~/focaApp/bclouder/vcna/vcna-invoice-scanner` — VCNA
- `~/focaApp/bclouder/vcna/vcna-invoice-email-reader`, `.../vcna-packing-slip-email-reader`, `.../vcna-reseller-email-reader` — VCNA e-mail intake Lambdas. **Most VCNA time lands here**, not in the scanner (ES-0006: 8.39 h in the readers, zero scanner commits). Find the real repos for a period by asking the ticks where the time was: `SELECT o.cwd, COUNT(DISTINCT t.ts) FROM ticks t JOIN observations o ON o.id=t.observation_id WHERE t.project_id=<id> AND t.ts>=$START AND t.ts<$END GROUP BY o.cwd;`
- `~/focaApp/bclouder/rumo/rumo-mobile` — Rumo

```bash
# Use commit TIMES so edge days are attributed to the right invoice window:
git -C <repo> log --all --no-merges --since=<START> --until=<END> --date=format:'%Y-%m-%d %H:%M' --pretty='%cd | %an | %s'
```
Use `--all` (all worktree branches) and NO `--author` filter — commits span multiple identities (`jhionan@gmail.com` *and* `37809501+jhionan@users.noreply.github.com`) plus teammates.

⚠️ **Then filter by author before writing a single line.** Unfiltered is right for *seeing* the period; it is wrong for *describing* it. The repos are shared, and a teammate can out-commit you in one of them — in ES-0006 the front-end held 114 of yours, **102 of Jéssica's** (`jessica.mlb@gmail.com` — the `tema:`/`redesign:`/`grids:` and GeVia Ordem-de-Serviço work) and 3 of Murilo's (`murilo.nerone@bclouder.com` — geolocation, report caminhão, scoping PDF). Describing their commits as your work bills the client for someone else's output. Get the split first, then read only your own:
```bash
git -C <repo> log --all --no-merges --since=<START> --until=<END> --pretty='%an <%ae>' | sort | uniq -c | sort -rn
git -C <repo> log --all --no-merges --author='jhionan@gmail.com' --since=<START> --until=<END> --date=format:'%Y-%m-%d %H:%M' --pretty='%cd | %s'
```
(Caught only because Rian said so mid-run on ES-0006 — the check was not in this runbook.)

**Source B — Claude Code console/transcript history** (what you actually worked on — catches design, debugging, and low-commit days that commits alone miss). Extract the per-day prompts:

```bash
cd ~/.claude/projects
python3 - <<'PY'
import glob, json, collections, datetime
by_day = collections.defaultdict(list)
for f in glob.glob("./-Users-rian-focaApp-bclouder*/*.jsonl"):
    for line in open(f, errors="ignore"):
        if '"type":"user"' not in line: continue
        try: o = json.loads(line)
        except: continue
        if o.get("type") != "user": continue
        c = o.get("message", {}).get("content")
        text = c if isinstance(c, str) else next((b.get("text") for b in c if isinstance(b, dict) and b.get("type")=="text"), None) if isinstance(c, list) else None
        ts = o.get("timestamp")
        if not text or not ts or text.strip().startswith(("<","Caveat:")): continue
        try: day = datetime.datetime.fromisoformat(ts.replace("Z","+00:00")).astimezone().date()
        except: continue
        by_day[day].append(text.replace("\n"," ")[:130])
for day in sorted(by_day):
    if not (datetime.date(2026,6,16) <= day <= datetime.date(2026,7,1)): continue   # <-- set to the invoice window
    print(f"\n==== {day} ({len(by_day[day])} prompts) ====")
    seen=set()
    for m in by_day[day]:
        if m[:40].lower() in seen: continue
        seen.add(m[:40].lower()); print("  •", m)
        if len(seen) >= 7: break
PY
```

⚠️ **The `seen`/`>= 7` cap above hides the secondary project's day.** It prints the loudest ~7 prompts per day, and on a day dominated by one product the other product's work vanishes — on ES-0006 that turned 3.09 h of VCNA production deployment into "documented the routing map". Whenever a day carries hours on **more than one project** (check the per-day per-project breakdown from Step 4), re-run the extractor **scoped to that project's transcript folder and uncapped**:
```bash
# e.g. VCNA only: glob "./-Users-rian-focaApp-bclouder-vcna*/*.jsonl", drop the `if len(seen) >= 7: break`
```
Also exclude the automated `"Review this change for security vulnerabilities"` prompts when judging whether a day has real transcript evidence — they fire from a hook, not from you, and a day can look "covered" while holding nothing but those.

**Reconcile each day against both sources, then write the Work column in client-facing prose** (see ES-0002/ES-0003 for tone): product-prefixed (`DaaS —`, `Rumo —`, `VCNA —`), benefit/feature-oriented, **no internal jargon** (no migration numbers, class names, branch names, cwd paths). One line per day. Fold manual entries (e.g. meetings logged via tick backfill) into that day's note.

**Source C — shell history** (`~/.zsh_history`, weak/corroborating only). Extended history is on, so lines are `: <epoch>:<dur>;<cmd>` and can be windowed:
```bash
python3 -c "
import re,datetime
for l in open('$HOME/.zsh_history','rb'):
    m=re.match(r'^: (\d+):\d+;(.*)$', l.decode('utf-8','replace').rstrip())
    if m and $START <= int(m.group(1)) < $END:
        print(datetime.datetime.fromtimestamp(int(m.group(1))), m.group(2)[:110])"
```
Expect mostly noise (`brew upgrade`, `atl`, `hunk`) — on ES-0006 it changed no line. Its two real uses: **corroborating** a day (`scrcpy -d` on 07-22 backs the Rumo on-device notification work) and **spotting non-BClouder work** that must not leak into the descriptions (`espaco-kids-export.sql`, `flutter run ... .env.patients.dev`). Never write a line from Source C alone.

**Anti-fabrication rule (learned the hard way on ES-0004 / 06-16):** if a day has tracked hours but you find NO commits and NO transcript activity for it, do NOT invent a plausible description — say the work is uncaptured, or ask. A fabricated line nearly went to the client. Every line must trace to Source A or Source B.

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
