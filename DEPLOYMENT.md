# Deploying to a VPS

This runs jobhunt on a Linux server with HTTPS, so you can open the board in any browser and connect Claude, ChatGPT and Claude Code from anywhere. Both the board and MCP ask for one password.

Commands marked **(PC)** run in PowerShell on your Windows machine. Commands marked **(VPS)** run in an SSH session on the server.

## 1. Server and domain

- **Server:** Ubuntu 24.04 with 2 GB of RAM. The image builds the web app and the Go binary on the server, and 1 GB can run out of memory during that build.
- **Domain:** in your DNS settings, add an A record for a subdomain such as `jobhunt.yourdomain.com` pointing at the server's IP. If other apps will share the server, give each one its own subdomain the same way.

Check the record has taken effect **(PC)**. It should print the server's IP:

```bash
nslookup jobhunt.yourdomain.com
```

## 2. Prepare the server

Log in **(PC)**:

```bash
ssh root@<vps-ip>
```

Update the system, open the firewall for SSH and the web ports, and install Docker **(VPS)**:

```bash
apt update && apt upgrade -y
```

```bash
ufw allow OpenSSH && ufw allow 80 && ufw allow 443 && ufw --force enable
```

```bash
curl -fsSL https://get.docker.com | sh
```

## 3. Clone the repository

The repository is private, so give the server a read-only deploy key. Create a key **(VPS)**, pressing Enter at each prompt:

```bash
ssh-keygen -t ed25519 -C "jobhunt-vps" -f ~/.ssh/id_ed25519
```

```bash
cat ~/.ssh/id_ed25519.pub
```

On GitHub, open https://github.com/perdhevi/JobHunt/settings/keys, choose **Add deploy key**, paste the line, name it `vps`, and leave **Allow write access** unticked. Then clone **(VPS)**, answering `yes` to trust github.com:

```bash
git clone git@github.com:perdhevi/JobHunt.git ~/jobhunt
```

## 4. Configure

**(VPS)**

```bash
cd ~/jobhunt/deploy && cp .env.example .env
```

Generate a password:

```bash
openssl rand -base64 18
```

Edit `.env` with `nano .env` (save with Ctrl+O and Enter, exit with Ctrl+X):

```
COMPOSE_PROFILES=caddy
JOBHUNT_DOMAIN=jobhunt.yourdomain.com
JOBHUNT_AUTH_PASSWORD=<the generated password>
JOBHUNT_REMOTE_TOKEN=
JOBHUNT_REMOTE_BOARD=true
```

- **jobhunt is the only app on the server:** keep `COMPOSE_PROFILES=caddy`, which runs a bundled Caddy for HTTPS.
- **The server will host other apps too:** delete the `COMPOSE_PROFILES=caddy` line and set up the [shared proxy](#6-shared-proxy-only-when-hosting-other-apps) in step 6.
- **You'd rather not expose the board:** set `JOBHUNT_REMOTE_BOARD=false` and open it through an [SSH tunnel](#board-through-an-ssh-tunnel) instead. MCP still works.

## 5. Start jobhunt

**(VPS)** The first build takes a few minutes:

```bash
docker compose up -d --build --remove-orphans
```

```bash
docker compose logs jobhunt
```

The log should end with `remote MCP https://jobhunt.yourdomain.com/mcp (OAuth…)` and `remote board https://jobhunt.yourdomain.com (password sign-in)`.

## 6. Shared proxy (only when hosting other apps)

Skip this if you kept `COMPOSE_PROFILES=caddy`. Only one program can serve ports 80 and 443, so one Caddy serves every app and routes by subdomain. Each app publishes its port on `127.0.0.1` only, as jobhunt does with 8788.

**(VPS)**

```bash
mkdir -p ~/proxy && cd ~/proxy
```

Create `compose.yaml` with `nano compose.yaml`. Host networking lets Caddy reach every app on `127.0.0.1`:

```yaml
services:
  caddy:
    image: caddy:2.10-alpine
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
volumes:
  caddy_data:
  caddy_config:
```

Create `Caddyfile` with `nano Caddyfile`, one block per app:

```
jobhunt.yourdomain.com {
	reverse_proxy 127.0.0.1:8788
}

otherapp.yourdomain.com {
	reverse_proxy 127.0.0.1:3000
}
```

```bash
docker compose up -d
```

After editing the `Caddyfile` later, apply it with:

```bash
cd ~/proxy && docker compose exec -w /etc/caddy caddy caddy reload
```

## 7. Check HTTPS

**(PC)**

```bash
curl https://jobhunt.yourdomain.com/.well-known/oauth-authorization-server
```

JSON back means Caddy has a certificate. If it fails, read Caddy's log on the server: `docker compose logs caddy` in `~/jobhunt/deploy`, or in `~/proxy` for the shared proxy. The usual causes are DNS that hasn't taken effect yet and ports 80 or 443 not being open.

## 8. Move your data

Do this once. Stop the local copy **(PC, in the project folder)**, then wait until Google Drive shows it has finished syncing:

```bash
docker compose down
```

Make an import folder **(VPS)**:

```bash
mkdir -p ~/import
```

Copy only the database and attachments **(PC)**. Leave the `.journal` and `.lease` files behind:

```bash
scp "G:\My Drive\JobHunt\jobhunt.db" root@<vps-ip>:~/import/
```

```bash
scp -r "G:\My Drive\JobHunt\attachments" root@<vps-ip>:~/import/
```

Load them into the server's data volume **(VPS)**:

```bash
cd ~/import && docker compose -f ~/jobhunt/deploy/compose.yaml stop jobhunt
```

```bash
docker compose -f ~/jobhunt/deploy/compose.yaml run --rm --no-deps --user root -v "$PWD:/in:ro" --entrypoint sh jobhunt -c 'cp -r /in/. /data/ && chown -R 1000:1000 /data'
```

```bash
docker compose -f ~/jobhunt/deploy/compose.yaml start jobhunt
```

From now on the server holds the live data. Don't start jobhunt locally again, or the two copies drift apart. The Google Drive copy becomes a backup.

## 9. Open the board

Go to `https://jobhunt.yourdomain.com` and sign in with the password. The browser stays signed in for 30 days. **Sign out** is in the board's header.

## 10. Connect your apps

The connector URL is `https://jobhunt.yourdomain.com/mcp`, and each app asks for the password once:

- **Claude** (web, Desktop and mobile): Settings → Connectors → Add custom connector. Leave the OAuth fields empty.
- **ChatGPT:** Settings → Apps & Connectors → Advanced → Developer mode → Create, with authentication set to OAuth. OpenAI limits full MCP access by plan.
- **Claude Code (PC):** run this, then `/mcp` inside Claude Code to sign in:

```bash
claude mcp add --scope user --transport http jobhunt https://jobhunt.yourdomain.com/mcp
```

The repository's [.mcp.json](.mcp.json) points at the local `127.0.0.1:8787`. Change it to the server's URL or delete it, so Claude Code doesn't try the stopped local copy.

## Day to day

**Deploy an update.** Push from your PC, then **(VPS)**:

```bash
cd ~/jobhunt && git pull && cd deploy && docker compose up -d --build --remove-orphans
```

**Back up** the database and attachments into a dated folder **(VPS)**:

```bash
cd ~/jobhunt/deploy && docker compose cp jobhunt:/data ~/backup-$(date +%F)
```

**Sign everything out**, for example if the password leaks: change `JOBHUNT_AUTH_PASSWORD` in `~/jobhunt/deploy/.env`, then run `docker compose up -d` in that folder. Every browser and connected app has to sign in again.

**Too many wrong passwords** from one address block sign-in from that address for 15 minutes, on both the board and the connector page.

### Board through an SSH tunnel

With `JOBHUNT_REMOTE_BOARD=false`, or as a fallback, open the board without a sign-in through SSH **(PC)**. Keep the window open, then go to http://127.0.0.1:8787:

```bash
ssh -N -L 8787:127.0.0.1:8787 root@<vps-ip>
```
