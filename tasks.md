# Antitimely roadmap

The first Windows milestone runs the local tracker. The next milestones add optional server sync, a ClickUp connection, and task controls in the Windows notification area.

## 1. Windows compatibility

- [x] Reuse the Go daemon, SQLite history, and project rules on Windows.
- [x] Capture the foreground executable and window title, track keyboard and mouse idle time, and record focus ticks for allowlisted apps.
- [x] Give the CLI and daemon a same-user local named pipe. Keep the Unix socket on macOS.
- [x] Add per-user login startup and background logs with Windows Task Scheduler.
- [x] Gate foreground tracking on the active input desktop and a connected Windows session.
- [x] Verify that Task Scheduler starts the daemon in the signed-in session, replaces a killed daemon, and stops the replacement cleanly.
- [ ] Verify fresh sign-in, lock/unlock, sleep/resume, and Remote Desktop on supported Windows versions.
- [x] Resolve hosted packaged applications by their Windows Application User Model ID.
- [ ] Verify a shared-host packaged app and an elevated app in a live Windows desktop.
- [x] Port process-based agent tracking and verify drive-letter, case, UNC, and WSL path attribution for agent transcripts.
- [x] Add live Windows CLI status and build a per-user MSI with a signing path.
- [ ] Sign and verify the release MSI with the release certificate.

## 2. Go sync server

- [x] Create the Go sync server with a health RPC, database migrations, and account and device authentication.
- [x] Define a versioned sync API and stable IDs for device records. Decide which project metadata and time summaries sync, and keep raw window titles and file paths local by default.
- [x] Add an opt-in daemon sync worker with a durable local outbox, bounded batches, and retry/backoff. Keep recording to SQLite while offline.
- [x] Deduplicate replays by device and record ID. Define how to reconcile project edits, overlapping device time, and timestamped records before computing shared totals.
- [x] Verify offline recording and replay after a lost acknowledgement against an RPC test server.
- [ ] Deploy the server with HTTPS, persistent storage, backups, and operational logging.

## 3. ClickUp integration

Depends on server authentication in milestone 2.

- [x] Support a personal API token for the first integration and ClickUp OAuth for user sign-in. Store the connected user's credential securely on the server and support disconnecting it. [ClickUp authentication](https://developer.clickup.com/docs/authentication)
- [x] Resolve the connected ClickUp user and authorized workspaces. Fetch paginated tasks assigned to that user, including subtasks, and expose them through the Go server. [ClickUp task filters](https://developer.clickup.com/reference/getfilteredteamtasks)
- [x] Add a persistent task timer with start, switch, and stop actions. Define what happens across idle time, sleep, and restart. Save queued time locally when offline.
- [x] Let the user select a task and assign a date range or amount of previously tracked Antitimely time. Show the exact duration before submission and keep the original observations and billing totals intact.
- [x] Publish completed timer sessions and approved historical allocations as ClickUp time entries. Record the returned entry ID and reconcile ambiguous responses before retrying writes. Prevent double submission when timer time overlaps selected history. [ClickUp time entries](https://developer.clickup.com/reference/createatimeentry)
- [x] Test permission boundaries, revoked credentials, rate limiting, and ambiguous or duplicate submissions with a simulated ClickUp API.
- [ ] Verify with a live ClickUp test workspace.

## 4. Windows tray app

Depends on the local Windows daemon. The ClickUp APIs provide optional task selection and publishing.

- [x] Add an icon in the Windows notification area that opens a searchable list of assigned tasks, grouped by workspace.
- [x] Open the ClickUp description, attachments, and recent comments from the task title. Start the task timer directly from its Play button.
- [x] Let the user select a task, start or switch the timer, see the current task and elapsed time, and stop tracking.
- [x] Start a standalone timer with an optional name, and save completed sessions to local history without a ClickUp connection.
- [x] Add an “Assign tracked time” flow that shows available local time, lets the user choose a task and duration, and confirms the ClickUp submission.
- [x] Show daemon, connection, and sync status. Make queued entries and submission failures visible so users can tell what reached ClickUp.
- [x] Verify native tray initialization and timer persistence across a daemon crash.
- [ ] Verify tray close/reopen and Windows sign-in with a configured ClickUp workspace.
