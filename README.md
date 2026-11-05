# Job Hunt Pipeline

A local kanban board for job applications, with an MCP server so any AI harness can read and update the same data without sending an application twice.

- **Backend:** Go, one binary (`jobhunt.exe`) that embeds the UI. SQLite through `modernc.org/sqlite`, so no C compiler is needed.
- **Frontend:** React, TypeScript, Vite, dnd-kit.
- **Data:** one SQLite file plus an `attachments` folder beside it, at a path you choose.

## Run it

In Docker, which needs only Docker Desktop:

```powershell
.\scripts\docker-up.cmd -Build      # http://127.0.0.1:8787
```

Or directly on Windows, which needs Go and Node:

```powershell
.\scripts\build.ps1
.\bin\jobhunt.exe serve --open
```

Pick one. See [Running in Docker](#running-in-docker) for why they can't run together.

## Running in Docker

Run `.\scripts\docker-up.cmd` to start jobhunt, adding `-Build` after changing the code. Stop it with `docker compose down`. The board and MCP are published on loopback only.

**Run the script again after Docker Desktop or Google Drive restarts.** Docker Desktop doesn't share Google Drive's virtual drive with containers, so the script mounts it inside Docker's VM, and that mount doesn't survive a restart. A plain `docker compose up` skips this step. jobhunt then refuses to start, because Docker would otherwise give it an empty disk and a database there would never reach Drive.

### Change the database path

The container mounts the whole drive, so paths stay in the Windows form you see in Explorer. Set the path under **Settings** in the UI, or:

```powershell
docker compose exec jobhunt jobhunt config set-db "G:\My Drive\JobHunt\jobhunt.db"
docker compose restart jobhunt
```

The default is `<drive>:\My Drive\JobHunt\jobhunt.db`. The saved path lives in the `config` Docker volume, separately from the Windows `%AppData%` config.

If Drive is on a different letter, pass it once and it's remembered in `.env`, then update the path as above:

```powershell
.\scripts\docker-up.cmd -Drive H
```

### Never run Docker and Windows at once

SQLite's locks don't cross from Windows into a container. In testing, writing from both at once corrupted the database. To prevent that, each side keeps a `jobhunt.db.docker.lease` or `.windows.lease` file beside the database and refuses to open while the other side is active. After a crash, the lease clears within 30 seconds.

While Docker is running, MCP clients must connect over HTTP, not by starting `jobhunt.exe`. An old stdio entry fails with a clear error. Use `http://127.0.0.1:8787/mcp`, or for stdio-only clients:

```json
{ "mcpServers": { "jobhunt": { "command": "docker", "args": ["exec", "-i", "jobhunt", "jobhunt", "mcp"] } } }
```

To import a backup, copy it to the drive and run `docker compose exec jobhunt jobhunt import "/drive/My Drive/backup.json"`.

For a remote MCP listener behind a tunnel, add these to `.env` instead of `config.json`, then run the script again. See [Connect an AI harness](#connect-an-ai-harness) for what each setting does.

```
JOBHUNT_REMOTE_LISTEN=0.0.0.0:8788
JOBHUNT_PUBLIC_URL=https://<your-tunnel>
JOBHUNT_AUTH_PASSWORD=<at least 12 characters>
```

## Choose the database location

This section covers running on Windows. For Docker, see [Change the database path](#change-the-database-path).

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
- **Docker is a different case.** Several programs on one computer stops being safe if some are in Docker and some on Windows. The lease described under [Running in Docker](#never-run-docker-and-windows-at-once) blocks that.

The database uses `journal_mode=TRUNCATE`, not WAL. WAL keeps committed rows in a side file that Drive would sync separately from the `.db`, so the uploaded copy could be stale.

Attachments keep their real extension on disk because Drive for Desktop renames extensionless files based on their content.

## Connect an AI harness

The same tools are available over two transports.

**Local HTTP.** While the board runs, in Docker or on Windows, any streamable-HTTP client on this machine can use `http://127.0.0.1:8787/mcp`. Claude Code picks this up from [.mcp.json](.mcp.json) in this folder.

**Local stdio.** The client starts jobhunt itself, and the board doesn't need to be running. This suits Claude Desktop, Cursor and similar tools on a Windows setup. With Docker, use the `docker exec` entry above instead. To add it everywhere:

```bash
claude mcp add --scope user jobhunt -- "C:\temp\JobHunt\bin\jobhunt.exe" mcp
```

Claude Desktop, in `claude_desktop_config.json`:

```json
{ "mcpServers": { "jobhunt": { "command": "C:\\temp\\JobHunt\\bin\\jobhunt.exe", "args": ["mcp"] } } }
```

**Remote, with a login.** Claude (web, Desktop and mobile), ChatGPT and other cloud clients connect from the vendor's servers, so they need a public HTTPS address. jobhunt opens a second listener for them that serves MCP and asks for a password through OAuth, which is how those clients sign in. A proxy or tunnel pointed at it can't reach the UI or REST API unless you turn the board on, below. The simplest setup is [a VPS](#deploy-to-a-vps). To run it yourself, add this to `config.json` and point an HTTPS tunnel at port 8788 only, never at 8787:

```json
"remoteMcp": { "listen": "127.0.0.1:8788", "publicUrl": "https://<your-domain>", "password": "<at least 12 characters>" }
```

Then add the connector with the URL `https://<your-domain>/mcp`. Each client shows a sign-in page the first time, asking for that password:

- **Claude** (claude.ai, Desktop, mobile): Settings → Connectors → Add custom connector. Leave the OAuth client fields empty.
- **ChatGPT**: Settings → Apps & Connectors → Advanced → Developer mode, then Create, with authentication set to OAuth. OpenAI limits full MCP access by plan, so check what yours allows.
- **Claude Code**: `claude mcp add --scope user --transport http jobhunt https://<your-domain>/mcp`, then run `/mcp` to sign in.

Apps stay signed in for 90 days of disuse, across restarts. Each refresh hands the app a new refresh token, and if an old one comes back, someone else holds a copy, so that app is signed out. To sign out every app and browser while keeping the password, use **Settings → Sign out everywhere** on the remote board, or run `jobhunt auth signout-all` (`docker compose exec jobhunt jobhunt auth signout-all` in Docker). Changing the password does the same. So does deleting `auth.secret`, which jobhunt creates beside `config.json` to sign its tokens. Sign-in events are logged with an `auth:` prefix.

After five wrong passwords from one address, the sign-in page refuses that address for 15 minutes. After 50 from any mix of addresses, it refuses everyone for 15 minutes, while browsers and apps already signed in keep working.

Sign-in codes go only to Claude (`claude.ai`, `claude.com`), ChatGPT (`chatgpt.com`) and `localhost`, which is where Claude Code and MCP Inspector receive them. Registration is open, as these clients require, so without this list anyone could register an app named "Claude" that sends your sign-in to their own site. To allow another host, set `"redirectHosts": ["claude.ai", "app.example.com"]` in `remoteMcp` (`JOBHUNT_REDIRECT_HOSTS=claude.ai,app.example.com` in `.env`). The list replaces the default, and `localhost` is always allowed.

To use the board through the same address, add `"board": true` to `remoteMcp` (`JOBHUNT_REMOTE_BOARD=true` in `.env`). `https://<your-domain>/` then shows a sign-in page asking for the same password, and keeps the browser signed in for 30 days of disuse. **Sign out** in the board's header ends that session on the server too, so a copied cookie stops working. Changing the password signs the browser out too.

For a client that can send a header but can't do OAuth, also set `"token"` (at least 24 characters) and send `Authorization: Bearer <token>`. A token on its own, without `publicUrl` and `password`, still works as before, including the `/mcp/<token>` URL form. Treat either as a password: anyone holding it can read and change your pipeline. "Sign out everywhere" doesn't revoke it; removing it does.

In Docker, the `JOBHUNT_*` variables in `.env` always win over `config.json`, even when empty: emptying `JOBHUNT_REMOTE_TOKEN=` turns the token off. Saving **Settings** writes only the database path to `config.json`, never the password or token.

### Deploy to a VPS

[DEPLOYMENT.md](DEPLOYMENT.md) walks through running jobhunt on a VPS with HTTPS, key-only SSH, encrypted off-server backups, the board behind an SSH tunnel or a sign-in, and room for other apps on the same server. It ends by retiring the local copy, so the server stays the only source of truth. The files it uses are in [deploy/](deploy).

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

For a full copy of the database and attachments while jobhunt keeps running, `jobhunt backup > jobhunt.tar` writes a consistent tar archive. On a VPS, see [Back up](DEPLOYMENT.md#back-up).

## Develop

```powershell
go test ./...
.\bin\jobhunt.exe serve     # API on 8787
cd web; npm run dev         # UI with hot reload, proxied to the API
```

`go build` needs `web/dist` to exist, so run `npm run build` in `web` once on a fresh clone.

| Path | Holds |
|---|---|
| `cmd/jobhunt` | CLI and server startup |
| `internal/remote` | Remote listener: MCP with OAuth, and the board behind a sign-in |
| `internal/store` | Schema, rules, duplicate detection. Every writer goes through here. |
| `internal/api` | REST API and loopback protection |
| `internal/mcpserver` | MCP tools |
| `deploy` | VPS setup with Caddy |
| `web/src` | React board |
