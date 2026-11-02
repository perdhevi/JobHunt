# Job Hunt Pipeline

A local kanban board for job applications, with an MCP server so any AI harness can read and update the same data without sending an application twice.

- **Backend:** Go, one binary (`jobhunt.exe`) that embeds the UI. SQLite through `modernc.org/sqlite`, so no C compiler is needed.
- **Frontend:** React, TypeScript, Vite, dnd-kit.
- **Data:** one SQLite file plus an `attachments` folder beside it, at a path you choose.

## Run it

```powershell
.\scripts\build.ps1                 # needs Go and Node
.\bin\jobhunt.exe serve --open      # http://127.0.0.1:8787
```

## Choose the database location

The path lives in `%AppData%\JobHunt\config.json`, per machine, because a Drive letter can differ between computers.

```powershell
.\bin\jobhunt.exe config set-db "G:\My Drive\JobHunt\jobhunt.db"
.\bin\jobhunt.exe config
```

You can also change it under **Settings** in the UI, then restart. `JOBHUNT_DB` overrides the path for one run, and `JOBHUNT_CONFIG` points at a different config file. Changing the path doesn't move data: stop jobhunt and copy the `.db` and `attachments` folder yourself.

### Google Drive rules

SQLite on a synced folder is safe only within these limits:

- **Use one computer at a time.** Drive syncs whole files and doesn't understand SQLite locks. Writing from two machines before sync finishes produces a conflicted copy such as `jobhunt (1).db`, and neither copy has both sets of changes. Stop jobhunt and let Drive finish syncing before switching machines.
- **Several programs on one computer is fine.** The board and any number of MCP clients can run together. This was tested on a real `G:` mount with two processes writing at once.
- **Don't open the `.db` from the Drive website**, and don't delete `jobhunt.db-journal`.

The database uses `journal_mode=TRUNCATE`, not WAL. WAL keeps committed rows in a side file that Drive would sync separately from the `.db`, so the uploaded copy could be stale.

Attachments keep their real extension on disk because Drive for Desktop renames extensionless files based on their content.

## Connect an AI harness

The same tools are available over two transports.

**Local stdio.** The client starts jobhunt itself, and the board doesn't need to be running. This suits Claude Code, Claude Desktop, Cursor and similar tools. Claude Code picks up [.mcp.json](.mcp.json) in this folder. To add it everywhere:

```bash
claude mcp add --scope user jobhunt -- "C:\temp\JobHunt\bin\jobhunt.exe" mcp
```

Claude Desktop, in `claude_desktop_config.json`:

```json
{ "mcpServers": { "jobhunt": { "command": "C:\\temp\\JobHunt\\bin\\jobhunt.exe", "args": ["mcp"] } } }
```

**Local HTTP.** While `jobhunt serve` runs, any streamable-HTTP client on this machine can use `http://127.0.0.1:8787/mcp`.

**ChatGPT and other cloud clients.** These connect from the vendor's servers, so they can't reach `127.0.0.1`. Add a dedicated listener to `config.json`, then point an HTTPS tunnel at that port only:

```json
"remoteMcp": { "listen": "127.0.0.1:8788", "token": "<at least 24 random characters>" }
```

The connector URL is `https://<your-tunnel>/mcp/<token>`, or `/mcp` with `Authorization: Bearer <token>` where the client supports headers. This listener serves only MCP and refuses anything without the token, so a tunnel can't reach the UI or REST API. Treat the URL as a password: anyone holding it can read and change your pipeline. OpenAI limits full MCP access by plan, so check what yours allows. Don't point a tunnel at port 8787.

### Tools

| Tool | Purpose |
|---|---|
| `check_application` | Is this job already tracked or sent? Returns `verdict` and `safeToSend`. |
| `add_application` | Add a job. Refuses duplicates, so retries are safe. |
| `mark_sent` | Record that an application went out. A repeat call changes nothing. |
| `update_application` | Change stage, next step, outcome or notes. |
| `get_application`, `list_applications` | Read records, by id, `+ref` or filter. |
| `list_follow_ups`, `pipeline_summary` | What is due, and the headline numbers. |
| `list_documents` | Cover letter and resume versions. |

There is deliberately no delete tool. Deleting happens only in the UI.

## How duplicates are prevented

| Verdict | Meaning | `safeToSend` | `add_application` |
|---|---|---|---|
| `new` | Nothing similar | true | Creates |
| `already_tracked` | Same job, not sent yet | true | Refuses, returns the existing record |
| `already_sent` | Same job, already sent | false | Refuses |
| `possible_duplicate` | Same company, similar role | false | Refuses unless `confirm_distinct=true` |

Two records are the same job when either of these match:

- **Link.** Tracking parameters, `www`, `/apply` suffixes and fragments are ignored, and LinkedIn and Upwork links reduce to their job id.
- **Company and role.** Case, punctuation and legal suffixes are ignored, and `https://factory.com` counts as Factory.

Different roles at the same company aren't duplicates, but they're listed in `otherRoles`.

The check is enforced in the database too. Unique indexes cover both keys, so two clients racing can't both insert. Exact duplicates can't be forced over MCP. For a genuine re-application, choose **Add anyway** in the UI.

The guard covers what's recorded. An agent that submits a form without calling `check_application` first, or doesn't call `mark_sent` afterwards, leaves nothing to compare against. The MCP server tells clients to follow that order, and it can't enforce it.

If an agent edits a record while you have it open, your save is refused with a message rather than overwriting the change.

## Importing

```powershell
.\bin\jobhunt.exe import backup.json --files .\attachments-folder
```

Reads the JSON backup format from the Export panel. It's safe to re-run: existing records are left alone, and entries marked as samples are skipped.

## Develop

```powershell
go test ./...
.\bin\jobhunt.exe serve     # API on 8787
cd web; npm run dev         # UI with hot reload, proxied to the API
```

`go build` needs `web/dist` to exist, so run `npm run build` in `web` once on a fresh clone.

| Path | Holds |
|---|---|
| `cmd/jobhunt` | CLI, server startup, remote MCP listener |
| `internal/store` | Schema, rules, duplicate detection. Every writer goes through here. |
| `internal/api` | REST API and loopback protection |
| `internal/mcpserver` | MCP tools |
| `web/src` | React board |
