# Deploying to a VPS

This runs jobhunt on a Linux server with HTTPS, so you can connect Claude, ChatGPT and Claude Code from anywhere. Connecting an app asks for one password. The board stays off the internet by default and opens through an SSH tunnel, or you can serve it behind the same password.

Commands marked **(PC)** run in PowerShell on your Windows machine. Commands marked **(VPS)** run in an SSH session on the server.

## 1. Server and domain

- **Server:** Ubuntu 24.04 with 2 GB of RAM. The image builds the web app and the Go binary on the server, and 1 GB can run out of memory during that build.
- **Domain:** in your DNS settings, add an A record for a subdomain such as `jobhunt.yourdomain.com` pointing at the server's IP. If other apps will share the server, give each one its own subdomain the same way.

Check the record has taken effect **(PC)**. It should print the server's IP:

```bash
nslookup jobhunt.yourdomain.com
```

## 2. Prepare the server

### Sign in with a key

The server ends up holding your whole pipeline, so it should accept SSH keys only. Make a key if you don't have one **(PC)**, pressing Enter at each prompt:

```bash
ssh-keygen -t ed25519
```

Copy it to the server **(PC)**. This asks for the root password your provider gave you, one last time:

```bash
type $env:USERPROFILE\.ssh\id_ed25519.pub | ssh root@<vps-ip> "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys"
```

Log in **(PC)**. It shouldn't ask for a password now:

```bash
ssh root@<vps-ip>
```

Turn off password logins **(VPS)**. The `10-` prefix makes this file win over the provider's own settings in the same folder:

```bash
printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin prohibit-password\n' > /etc/ssh/sshd_config.d/10-jobhunt.conf && sshd -t && systemctl reload ssh
```

Keep this session open and check that a **new** window still logs in with `ssh root@<vps-ip>`. If it doesn't, delete that file from the open session and run `systemctl reload ssh`.

### Updates and firewall

Update the system and open the firewall for SSH and the web ports **(VPS)**:

```bash
apt update && apt upgrade -y
```

```bash
ufw allow OpenSSH && ufw allow 80 && ufw allow 443 && ufw --force enable
```

Ubuntu installs security updates by itself. Check it's on **(VPS)**; the output should say `active (running)`:

```bash
systemctl status unattended-upgrades --no-pager
```

### Install Docker

Install Docker from Docker's own package repository **(VPS)**, so `apt` verifies and updates it:

```bash
install -m 0755 -d /etc/apt/keyrings && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc && chmod a+r /etc/apt/keyrings/docker.asc
```

```bash
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
```

```bash
apt update && apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Check the version **(VPS)**. It should be 28 or later, because older versions let other machines on the provider's network reach ports published on `127.0.0.1`:

```bash
docker version --format '{{.Server.Version}}'
```

Docker writes its own firewall rules, so a port it publishes is open even where `ufw` says it's closed. That's why every port in this setup is published on `127.0.0.1` only. Keep it that way if you edit `compose.yaml`.

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

**(VPS)** The `.env` file will hold the password, so only root may read it:

```bash
cd ~/jobhunt/deploy && cp .env.example .env && chmod 600 .env
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
JOBHUNT_REMOTE_BOARD=false
JOBHUNT_REDIRECT_HOSTS=
```

- **jobhunt is the only app on the server:** keep `COMPOSE_PROFILES=caddy`, which runs a bundled Caddy for HTTPS.
- **The server will host other apps too:** delete the `COMPOSE_PROFILES=caddy` line and set up the [shared proxy](#6-shared-proxy-only-when-hosting-other-apps) in step 6.
- **You want the board in any browser, not only through a tunnel:** set `JOBHUNT_REMOTE_BOARD=true`. Then one password stands between the internet and your contacts, pay and resumes, so keep it the long generated one.
- **An app other than Claude or ChatGPT signs in from its own website:** add that site's host to `JOBHUNT_REDIRECT_HOSTS`, for example `JOBHUNT_REDIRECT_HOSTS=claude.ai,claude.com,chatgpt.com,app.example.com`. Apps that sign in through `localhost`, such as Claude Code, always work. Without this list, anyone could register an app named "Claude" that sends your sign-in to their own site.

## 5. Start jobhunt

**(VPS)** The first build takes a few minutes:

```bash
docker compose up -d --build --remove-orphans
```

```bash
docker compose logs jobhunt
```

The log should end with `remote MCP https://jobhunt.yourdomain.com/mcp (OAuth…)`, followed by `remote board https://jobhunt.yourdomain.com (password sign-in)` if you turned the board on.

## 6. Shared proxy (only when hosting other apps)

Skip this if you kept `COMPOSE_PROFILES=caddy`. Only one program can serve ports 80 and 443, so one Caddy serves every app and routes by subdomain. Each app publishes its port on `127.0.0.1` only, as jobhunt does with 8788. As step 2 explains, a port published any other way is open to the internet whatever `ufw` says.

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
	header Strict-Transport-Security "max-age=31536000"
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

Load them into the server's data volume, then delete the import folder so no second copy of your data stays on the disk **(VPS)**:

```bash
cd ~/jobhunt/deploy && docker compose stop jobhunt
```

```bash
docker run --rm --user root -v jobhunt_data:/data -v ~/import:/in:ro --entrypoint sh jobhunt:local -c 'cp -r /in/. /data/ && chown -R 1000:1000 /data'
```

```bash
docker compose start jobhunt && rm -rf ~/import
```

From now on the server holds the live data. Don't start jobhunt locally again, or the two copies drift apart. The Google Drive copy stops changing, so take [backups](#back-up) from the server instead.

## 9. Open the board

**Board on (`JOBHUNT_REMOTE_BOARD=true`):** go to `https://jobhunt.yourdomain.com` and sign in with the password. The browser stays signed in for 30 days of disuse. **Sign out** is in the board's header.

**Board off (the default):** open it through [an SSH tunnel](#board-through-an-ssh-tunnel).

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

**Deploy an update.** Push from your PC, then **(VPS)**. `--pull` fetches the latest Node, Go and Alpine base images, so the build picks up their security fixes:

```bash
cd ~/jobhunt && git pull && cd deploy && docker compose build --pull && docker compose up -d --remove-orphans
```

### Back up

A backup stops jobhunt for a few seconds, because copying the database while it's being written can catch it half-way. Copy the data out and restart **(VPS)**:

```bash
cd ~/jobhunt/deploy && docker compose stop jobhunt && docker cp jobhunt-jobhunt-1:/data ~/backup-$(date +%F); docker compose start jobhunt
```

Pack and encrypt it, and delete the plain copy **(VPS)**. `gpg` asks for a passphrase; keep it somewhere other than this server:

```bash
cd ~ && tar -czf jobhunt-$(date +%F).tar.gz backup-$(date +%F) && rm -rf backup-$(date +%F) && gpg --symmetric --cipher-algo AES256 jobhunt-$(date +%F).tar.gz && rm jobhunt-$(date +%F).tar.gz
```

A backup on the same disk dies with the server, so take a copy off it **(PC)**, for example into Google Drive:

```bash
scp "root@<vps-ip>:~/jobhunt-*.tar.gz.gpg" "G:\My Drive\JobHunt"
```

To restore, decrypt with `gpg -d jobhunt-<date>.tar.gz.gpg | tar -xz`, then load the `backup-<date>/` folder as in [step 8](#8-move-your-data), using it in place of `~/import`.

### Signing out

**A device was lost, or you suspect someone has access:** sign out every browser and app without changing the password, either from **Settings → Sign out everywhere** on the board, or **(VPS)**:

```bash
cd ~/jobhunt/deploy && docker compose exec jobhunt jobhunt auth signout-all
```

Each app asks for the password again the next time it connects. The static token, if you set one, isn't affected; remove it from `.env` and run `docker compose up -d` to revoke it.

**The password leaked:** change `JOBHUNT_AUTH_PASSWORD` in `~/jobhunt/deploy/.env`, then run `docker compose up -d` in that folder. That also signs everything out.

**An app that reuses an old refresh token** is signed out by itself, because that means someone else holds a copy. Reconnect it.

**Too many wrong passwords** from one address block sign-in from that address for 15 minutes, on both the board and the connector page. After 50 wrong passwords from any mix of addresses, sign-in is blocked for everyone for 15 minutes. Browsers and apps already signed in keep working. The log shows each attempt: `docker compose logs jobhunt | grep auth:`.

### Board through an SSH tunnel

The board on port 8787 has no sign-in, so it isn't published on the server at all. Instead, tunnel straight to the container. Find its address **(VPS)**:

```bash
docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' jobhunt-jobhunt-1
```

Open the tunnel with that address **(PC)**, keep the window open, then go to http://127.0.0.1:8787:

```bash
ssh -N -L 8787:<container-address>:8787 root@<vps-ip>
```

The address can change when the container is recreated, after an update for example. Look it up again if the tunnel stops connecting.
