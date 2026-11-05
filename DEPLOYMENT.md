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

Check that the key really is Docker's before apt trusts it. Docker's install documentation gives its fingerprint as `9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88`. This prints `Key OK`, or deletes the key if it's a different one **(VPS)**:

```bash
gpg --show-keys --with-colons /etc/apt/keyrings/docker.asc | grep -q '^fpr:*9DC858229FC7DD38854AE2D88D81803C0EBFCD88:' && echo "Key OK" || { rm -f /etc/apt/keyrings/docker.asc; echo "Wrong key, deleted. Stop here."; }
```

Go on only after `Key OK`. If the key was deleted, the install below fails rather than trusting it.

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

Docker keeps every container's log forever unless told otherwise, and on a server shared by several apps one chatty app can fill the disk. jobhunt's `compose.yaml` caps its own logs; this makes the cap the default for every app, and keeps containers running while Docker itself updates **(VPS)**:

```bash
printf '{\n  "log-driver": "local",\n  "live-restore": true\n}\n' > /etc/docker/daemon.json && systemctl restart docker
```

A 2 GB server running several apps, and building images, is better off with some swap **(VPS)**:

```bash
fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile && echo '/swapfile none swap sw 0 0' >> /etc/fstab
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

The log should end with `remote MCP https://jobhunt.yourdomain.com/mcp (OAuth…)`, followed by `remote board https://jobhunt.yourdomain.com (password sign-in)` if you turned the board on. Within a minute, `docker compose ps` should show jobhunt as `(healthy)`.

## 6. Shared proxy (only when hosting other apps)

Skip this if you kept `COMPOSE_PROFILES=caddy`. Only one program can serve ports 80 and 443, so one Caddy serves every app and routes by subdomain. Each app publishes its port on `127.0.0.1` only, as jobhunt does with 8788. As step 2 explains, a port published any other way is open to the internet whatever `ufw` says.

**(VPS)**

```bash
mkdir -p ~/proxy && cd ~/proxy
```

Create `compose.yaml` with `nano compose.yaml`. Host networking lets Caddy reach every app on `127.0.0.1`. Caddy faces the internet, so it keeps only the right to bind ports 80 and 443:

```yaml
services:
  caddy:
    image: caddy:2.10-alpine
    restart: unless-stopped
    network_mode: host
    cap_drop: [ALL]
    cap_add: [NET_BIND_SERVICE]
    security_opt: [no-new-privileges:true]
    mem_limit: 256m
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
volumes:
  caddy_data:
  caddy_config:
```

Create `Caddyfile` with `nano Caddyfile`, one block per app. The snippet at the top holds what every site gets:

```
(common) {
	encode zstd gzip
	header {
		Strict-Transport-Security "max-age=31536000"
		X-Content-Type-Options nosniff
		-Server
	}
}

jobhunt.yourdomain.com {
	import common
	request_body {
		max_size 25MB
	}
	reverse_proxy 127.0.0.1:8788
}

otherapp.yourdomain.com {
	import common
	reverse_proxy 127.0.0.1:3000
}
```

Give every other app the same care jobhunt's `compose.yaml` takes:

- **Publish ports on `127.0.0.1` only**, such as `"127.0.0.1:3000:3000"`. Anything else is open to the internet.
- **Cap memory and processes**, with `mem_limit` and `pids_limit`, so one app can't starve the rest.
- **Give each app its own compose project.** Each gets its own network, and Docker keeps containers on different networks from reaching each other. Don't attach apps to one shared network.
- **Put nothing that lacks a sign-in on a port.** Every process on the server, and Caddy with it, can reach every port published on `127.0.0.1`. That's why jobhunt's board without a sign-in sits on a socket instead.

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

Do this once. First note two numbers, so you can check the move in [step 11](#11-retire-the-local-copy): how many applications the board lists, shown at the foot of the board, and how many attachment files there are **(PC)**:

```bash
(Get-ChildItem "G:\My Drive\JobHunt\attachments").Count
```

Stop the local copy **(PC, in the project folder)**, then wait until Google Drive shows it has finished syncing:

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

From now on the server holds the live data, and the Google Drive copy stops changing. Don't start jobhunt locally again, or the two copies drift apart. [Step 11](#11-retire-the-local-copy) makes sure nothing on your PC opens the old copy by accident.

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

## 11. Retire the local copy

The server now holds the only live copy of your data. Anything on your PC that still opens the Google Drive database would start a second copy that drifts apart from it. The leases that stop Windows and Docker from writing at once can't see the server, so they won't catch this. Google Drive was also your copy off the server, so from now on the server's backups are the only other copy.

Work through these in order, and delete nothing until the first two checks pass.

**Check the move.** The board on the server should list as many applications as you noted in step 8; ask a connected app for its pipeline summary if the board is off. Then count the attachments, which should match too **(VPS)**:

```bash
cd ~/jobhunt/deploy && docker compose exec jobhunt ls /data/attachments | wc -l
```

**Back up, and check that the backup opens.** Take a backup and copy it off the server as in [Back up](#back-up). Then decrypt and unpack it into a scratch folder **(VPS)**. The list should show `jobhunt.db` and the `attachments` folder:

```bash
cd ~ && mkdir -p restore-test && gpg -d jobhunt-$(date +%F).tar.gpg | tar -x -C restore-test && ls -lR restore-test && rm -rf restore-test
```

**Point jobhunt on your PC at a practice database.** `bin\jobhunt.exe`, any app that starts `jobhunt.exe mcp` itself, and this repository's `.claude/launch.json` all read the database path from `%AppData%\JobHunt\config.json`, which still names the Google Drive file. Point it at a database for development only **(PC, in the project folder)**:

```bash
.\bin\jobhunt.exe config set-db "$env:LOCALAPPDATA\JobHunt-dev\jobhunt.db"
```

Also remove any `jobhunt.exe mcp` or `docker exec … jobhunt mcp` entry from apps such as Claude Desktop or Cursor, and connect them to the server as in step 10.

**Remove the local Docker setup.** The container stopped in step 8. Remove what it left behind **(PC, in the project folder)**. The `jobhunt_config` volume holds only local settings and the local sign-in secret, none of your data:

```bash
docker volume rm jobhunt_config
```

```bash
docker image rm jobhunt:local
```

Then delete the `.env` file in the project folder, which only records the drive letter.

**Archive the Google Drive copy.** In Explorer, rename `G:\My Drive\JobHunt` to something like `JobHunt archive 2026-11`. Keep it until a few server backups have restored, then delete it. Server backups go into a folder of their own, as [Back up](#back-up) shows.

**Repeat on any other computer** that ever ran jobhunt against the Google Drive folder: point its config at a practice database and remove its local Docker setup.

## Day to day

**Deploy an update.** Push from your PC, then **(VPS)**. `--pull` fetches the latest Node, Go and Alpine base images, so the build picks up their security fixes:

```bash
cd ~/jobhunt && git pull && cd deploy && docker compose build --pull && docker compose up -d --remove-orphans
```

### Back up

`jobhunt backup` writes the database and attachments as one archive while jobhunt keeps running. It copies the database inside a single read, so the copy is consistent even if an app writes meanwhile **(VPS)**:

```bash
cd ~/jobhunt/deploy && docker compose exec -T jobhunt jobhunt backup > ~/jobhunt-$(date +%F).tar
```

Encrypt it and delete the plain copy **(VPS)**. `gpg` asks for a passphrase; keep it somewhere other than this server:

```bash
cd ~ && gpg --symmetric --cipher-algo AES256 jobhunt-$(date +%F).tar && rm jobhunt-$(date +%F).tar
```

A backup on the same disk dies with the server, so take a copy off it **(PC)**, for example into a folder of its own in Google Drive. Create the folder once:

```bash
New-Item -ItemType Directory -Force "G:\My Drive\JobHunt backups"
```

```bash
scp "root@<vps-ip>:~/jobhunt-*.tar.gpg" "G:\My Drive\JobHunt backups"
```

To restore, unpack it into a folder **(VPS)**, then load that folder as in [step 8](#8-move-your-data), in place of `~/import`:

```bash
mkdir -p ~/import && gpg -d jobhunt-<date>.tar.gpg | tar -x -C ~/import
```

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

The board without a sign-in has no port on the server. It listens on a socket file that only root can open, so neither other apps' containers nor other processes on the server can reach it. SSH can forward a port on your PC to that socket. Open the tunnel **(PC)**, keep the window open, then go to http://127.0.0.1:8787:

```bash
ssh -N -L 8787:/var/lib/docker/volumes/jobhunt_run/_data/board.sock root@<vps-ip>
```

The path stays the same when the container is recreated, after an update for example. If the tunnel connects but the page doesn't load, check that jobhunt is running with `docker compose ps` **(VPS)**.
