# Hardened Containerized Vaultwarden

[![CI](https://github.com/underhax/vaultwarden-docker/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/vaultwarden-docker/actions/workflows/ci.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/vaultwarden-docker)](https://github.com/underhax/vaultwarden-docker/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/vaultwarden-docker)](https://github.com/underhax/vaultwarden-docker/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/vaultwarden-docker)](https://github.com/underhax/vaultwarden-docker)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Hardened, containerized deployment architecture for [Vaultwarden](https://github.com/dani-garcia/vaultwarden) packaged in a minimal `scratch` runtime with PostgreSQL storage over an isolated `tmpfs` UNIX domain socket, host Discretionary Access Control (DAC) boundaries, Docker secret ingestion via a dedicated Go init supervisor, and a standalone binary healthcheck probe.

## Architecture Highlights

This deployment packages Vaultwarden into an isolated, defense-in-depth runtime environment:

* Scratch runtime: Built `FROM scratch` containing only the upstream Vaultwarden binary, `web-vault` UI assets, CA certificates, and two static Go helpers: `init` (process supervision, signal handling, and secret ingestion) and `probe` (HTTP healthchecks). The container contains no shell or system utilities.
* In-memory secret ingestion: Database credentials are read from secrets to construct a URL-encoded `DATABASE_URL`. Additionally, any secret file prefixed with `vw_` is automatically stripped and converted to an uppercase environment variable (for example, `vw_admin_token` becomes `ADMIN_TOKEN`), allowing any standard Vaultwarden parameter to be securely passed via Docker secrets into process memory.
* Socket-only database isolation: PostgreSQL executes with container networking completely disabled, communicating exclusively over an in-memory UNIX domain socket mounted on tmpfs. Eliminates TCP overhead, exposed database ports, and lateral network attack vectors.
* Decoupled RSA private key: The RSA private key is managed strictly as a read-only Docker secret rather than stored in the general application data directory, enforcing host DAC boundaries and preventing key leakage in routine volume backups.
* Least privilege enforcement: Containers execute under dedicated unprivileged non-root user accounts with read-only root filesystems, all Linux kernel capabilities dropped, privilege escalation blocked, and strict kernel resource boundaries.

---

<details>
<summary><strong>Directory Structure</strong></summary>

Production directory layout on the host (`/home/vaultwarden`):

```text
├── docker-compose.yaml                                        # Production deployment manifest
├── .env                                                       # Host infrastructure and service configuration
├── secrets/                                                   # Docker secrets directory (root:vaultwarden 0750)
│   ├── db_name.txt                                            # PostgreSQL database name
│   ├── db_user.txt                                            # PostgreSQL username
│   ├── db_password.txt                                        # PostgreSQL password
│   ├── rsa_key.pem                                            # Instance RSA 4096-bit private key
│   ├── vw_domain.txt                                          # Optional: Fully qualified domain URL
│   ├── vw_admin_token.txt                                     # Optional: Admin panel access token
│   ├── vw_org_creation_users.txt                              # Optional: Users permitted to create organizations
│   ├── vw_signups_domains_whitelist.txt                       # Optional: Allowed email domains for registration
│   ├── vw_smtp_host.txt                                       # Optional: SMTP server hostname
│   ├── vw_smtp_from.txt                                       # Optional: SMTP sender email address
│   ├── vw_smtp_port.txt                                       # Optional: SMTP server port
│   ├── vw_smtp_username.txt                                   # Optional: SMTP authentication username
│   ├── vw_smtp_password.txt                                   # Optional: SMTP authentication password
│   ├── vw_helo_name.txt                                       # Optional: SMTP HELO hostname
│   ├── vw_push_installation_id.txt                            # Optional: Bitwarden push installation ID
│   ├── vw_push_installation_key.txt                           # Optional: Bitwarden push installation key
│   ├── vw_push_relay_uri.txt                                  # Optional: Bitwarden push relay URI
│   └── vw_push_identity_uri.txt                               # Optional: Bitwarden push identity URI
├── postgres/                                                  # Persistent PostgreSQL storage
├── data/                                                      # Vaultwarden application data (attachments, sends)
└── logs/                                                      # Vaultwarden application logs
```

</details>

---

<details>
<summary><strong>Deployment &amp; Setup</strong></summary>

### Prerequisites

* **Docker Engine & Compose:** Ensure Docker Engine and the Docker Compose plugin are installed on the host. Follow the official installation guide for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository).
* **External Gateway Network:** The stack communicates with the perimeter reverse proxy over an external Docker network named `frontend_gateway`. If utilizing the **[angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose)** stack, this network is created automatically. Otherwise, create it manually:

  ```bash
  docker network create --subnet=172.20.0.0/16 frontend_gateway
  ```

### 1. Create a Dedicated System User

Choose a dedicated, unprivileged numeric ID unique to this service on your host and verify that it is not already in use by another user or group:

```bash
APP_UID=<CHOSEN_UID>

getent passwd ${APP_UID}
getent group ${APP_UID}
```

> **Important:** Both commands must return empty output. Each containerized service must have its own dedicated unprivileged user and group to enforce strict host DAC privilege separation between different stacks.

Create an unprivileged system account with a matching user-group (`-U`) and disabled login shell:

```bash
sudo useradd -m -d /home/vaultwarden -s /usr/sbin/nologin -u ${APP_UID} -U vaultwarden
```

### 2. Create Directory Layout & Assign Ownership

Create the base directory and assign ownership:

```bash
sudo mkdir -p /home/vaultwarden
sudo chown root:vaultwarden /home/vaultwarden
sudo chmod 0750 /home/vaultwarden
```

Create runtime subdirectories from `root` and assign strict DAC permissions:

```bash
sudo mkdir -p /home/vaultwarden/{data,logs,postgres,secrets}

sudo chown root:vaultwarden /home/vaultwarden/secrets
sudo chmod 0750 /home/vaultwarden/secrets

sudo chown vaultwarden:vaultwarden /home/vaultwarden/{data,logs,postgres}
sudo chmod 0700 /home/vaultwarden/{data,logs,postgres}
```

> **Log Access for Non-Root Services (Optional):** By default, `logs/` is restricted to `0700`. If an external unprivileged service (such as CrowdSec running under a non-root account) requires access to parse application logs, grant read permissions to the directory and log files:

```bash
sudo chmod 0755 /home/vaultwarden/logs
sudo chmod 0644 /home/vaultwarden/logs/*
```

### 3. Deploy Project Files

Download the production manifests directly into `/home/vaultwarden`:

```bash
REPO="https://raw.githubusercontent.com/underhax/vaultwarden-docker/main"

sudo curl -fsSL ${REPO}/docker-compose.yaml -o /home/vaultwarden/docker-compose.yaml
sudo curl -fsSL ${REPO}/.env.example -o /home/vaultwarden/.env

sudo chown root:root /home/vaultwarden/{docker-compose.yaml,.env}
sudo chmod 0600 /home/vaultwarden/{docker-compose.yaml,.env}
```

### 4. Configure Host Environment Settings

```bash
sed -i "s/^APP_UID=.*/APP_UID=${APP_UID}/" /home/vaultwarden/.env
sed -i "s/^APP_GID=.*/APP_GID=${APP_UID}/" /home/vaultwarden/.env
```

Edit `/home/vaultwarden/.env` using a text editor:

```bash
sudo nano /home/vaultwarden/.env
```

Optional host parameters (utilized only when direct host port publishing is uncommented in `docker-compose.yaml` for initial setup or out-of-band administration when `/admin` is blocked at the perimeter reverse proxy):
* `HOST_BIND_IP`: Host network interface to bind. Defaults to `127.0.0.1` (loopback only) for private access via SSH tunnel. For WireGuard VPN, set this to the host's WireGuard interface IP (e.g. `10.8.0.1`).
* `HOST_PORT`: Unique unreserved host port (default `55080`, mapped to container internal port `8080`).

### 5. Generate Secrets

Install credential generation tools:

```bash
sudo apt update && sudo apt install -y pwgen openssl
```

**Database credentials:**

Initialize secret files with strict DAC permissions before writing:

```bash
sudo touch /home/vaultwarden/secrets/{db_name,db_user,db_password}.txt
sudo chown root:vaultwarden /home/vaultwarden/secrets/{db_name,db_user,db_password}.txt
sudo chmod 0440 /home/vaultwarden/secrets/{db_name,db_user,db_password}.txt

printf "db_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/vaultwarden/secrets/db_name.txt > /dev/null
printf "u_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/vaultwarden/secrets/db_user.txt > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/vaultwarden/secrets/db_password.txt > /dev/null
```

**Instance RSA private key:**

Generate a 4096-bit RSA private key directly into the secrets directory:

```bash
sudo openssl genrsa -out /home/vaultwarden/secrets/rsa_key.pem 4096
sudo chown root:vaultwarden /home/vaultwarden/secrets/rsa_key.pem
sudo chmod 0440 /home/vaultwarden/secrets/rsa_key.pem
```

> **Note:** If migrating from an existing Vaultwarden installation and you want to preserve active client sessions without requiring re-authentication across all devices, copy your existing private key to `/home/vaultwarden/secrets/rsa_key.pem` with `root:vaultwarden` ownership and `0440` permissions. Generating a new key is safe for vault data, but will prompt connected clients to log in again.

**Optional: Administrator panel access token:**

To secure the `/admin` portal, Vaultwarden requires an Argon2id password hash.

> **Note:** Enter and confirm your administrator password at the prompt. The generated Argon2id hash is written directly into the secret file without manual copy-pasting, and the isolated subshell purges the memory variable upon exit. Uncomment `vw_admin_token` in `docker-compose.yaml` (both under `services.vaultwarden.secrets` and the root `secrets:` block).

Initialize the secret file with restricted DAC permissions, then generate and write the hash directly inside an isolated subshell:

```bash
sudo touch /home/vaultwarden/secrets/vw_admin_token.txt
sudo chown root:vaultwarden /home/vaultwarden/secrets/vw_admin_token.txt
sudo chmod 0440 /home/vaultwarden/secrets/vw_admin_token.txt

(
  TOKEN=$(sudo docker run --rm -it --entrypoint /usr/local/bin/vaultwarden ghcr.io/underhax/vaultwarden-docker:latest hash)
  printf '%s' "${TOKEN}" | tr -d '\r\n' | sudo tee /home/vaultwarden/secrets/vw_admin_token.txt > /dev/null
)

sudo nano /home/vaultwarden/docker-compose.yaml
```

**Optional: Public domain URL:**

Configures the primary public vault URL. The URL in `vw_domain.txt` must match your reverse proxy deployment model (detailed in the External Angie section below):
* Root Domain (Option A): Specify the origin without a trailing slash (e.g., `https://vault.example.com`).
* Sub-Directory (Option B): Specify the full URL including your chosen custom subpath (e.g., `https://example.com/<CUSTOM_SUBPATH>`). This must match the location prefix configured in your reverse proxy template.

> **Note:** Uncomment `vw_domain` in `docker-compose.yaml` (both under `services.vaultwarden.secrets` and the root `secrets:` block).

```bash
sudo touch /home/vaultwarden/secrets/vw_domain.txt
sudo chown root:vaultwarden /home/vaultwarden/secrets/vw_domain.txt
sudo chmod 0440 /home/vaultwarden/secrets/vw_domain.txt

sudo nano /home/vaultwarden/secrets/vw_domain.txt
sudo nano /home/vaultwarden/docker-compose.yaml
```

**Optional: SMTP configuration:**

Configures outbound SMTP server credentials and delivery parameters.

> **Note:** Populate server parameters and credentials in each respective secret file. Then uncomment the SMTP secrets in `docker-compose.yaml` (both under `services.vaultwarden.secrets` and the root `secrets:` block). Additional transport parameters (such as `SMTP_SECURITY`) can be configured in `/home/vaultwarden/.env`.

```bash
sudo touch /home/vaultwarden/secrets/{vw_smtp_host,vw_smtp_from,vw_smtp_port,vw_smtp_username,vw_smtp_password,vw_helo_name}.txt
sudo chown root:vaultwarden /home/vaultwarden/secrets/vw_smtp_*.txt /home/vaultwarden/secrets/vw_helo_name.txt
sudo chmod 0440 /home/vaultwarden/secrets/vw_smtp_*.txt /home/vaultwarden/secrets/vw_helo_name.txt

sudo nano /home/vaultwarden/secrets/vw_smtp_host.txt
sudo nano /home/vaultwarden/secrets/vw_smtp_from.txt
sudo nano /home/vaultwarden/secrets/vw_smtp_port.txt
sudo nano /home/vaultwarden/secrets/vw_smtp_username.txt
sudo nano /home/vaultwarden/secrets/vw_smtp_password.txt
sudo nano /home/vaultwarden/secrets/vw_helo_name.txt
sudo nano /home/vaultwarden/docker-compose.yaml
```

**Optional: Bitwarden push notifications:**

Configures real-time device synchronization and prompt notifications via Bitwarden push servers.

> **Note:** Consult the [Vaultwarden Push Notifications Guide](https://github.com/dani-garcia/vaultwarden/wiki/Enabling-Mobile-Client-push-notification) for credentials and relay endpoints. Populate the values in their respective secret files, then uncomment the push secrets in `docker-compose.yaml` (both under `services.vaultwarden.secrets` and the root `secrets:` block).

```bash
sudo touch /home/vaultwarden/secrets/vw_push_{installation_id,installation_key,relay_uri,identity_uri}.txt
sudo chown root:vaultwarden /home/vaultwarden/secrets/vw_push_*.txt
sudo chmod 0440 /home/vaultwarden/secrets/vw_push_*.txt

sudo nano /home/vaultwarden/secrets/vw_push_installation_id.txt
sudo nano /home/vaultwarden/secrets/vw_push_installation_key.txt
sudo nano /home/vaultwarden/secrets/vw_push_relay_uri.txt
sudo nano /home/vaultwarden/secrets/vw_push_identity_uri.txt
sudo nano /home/vaultwarden/docker-compose.yaml
```

### 6. Start the Stack

Pull images and start the services:

```bash
sudo docker compose -f /home/vaultwarden/docker-compose.yaml pull
sudo docker compose -f /home/vaultwarden/docker-compose.yaml up -d
```

### 7. Private Administration Access Recipes

When access to the `/admin` portal is locked down at the perimeter reverse proxy (recommended best practice), administration is performed out-of-band over an isolated loopback binding. If you uncomment the port binding (`HOST_BIND_IP:HOST_PORT`) in `docker-compose.yaml`, the administration portal can be reached securely on `127.0.0.1:55080/admin`:

* **Access via SSH Tunnel (Default — 127.0.0.1):**
  Forward the host port securely over SSH:
  ```bash
  ssh -i ~/.ssh/id_ed25519 -p 22 -N -L 55080:127.0.0.1:55080 user@your-server-ip
  ```
  Then open `http://127.0.0.1:55080/admin` in your local browser to access the administrator panel.

* **Access via WireGuard VPN:**
  In `/home/vaultwarden/.env`, set `HOST_BIND_IP` to your host's WireGuard interface address (e.g. `HOST_BIND_IP='10.8.0.1'`). Authorized VPN clients can then navigate directly to `http://10.8.0.1:55080/admin`.

</details>

---

<details>
<summary><strong>External Angie (Perimeter Reverse Proxy)</strong></summary>

### Edge Reverse Proxy Configuration

The `vaultwarden` container connects to the perimeter reverse proxy over the external `frontend_gateway` network (`172.20.0.0/16`) without publishing public HTTP ports on the host. An external Angie instance terminates TLS, manages Let's Encrypt certificates, and routes traffic over HTTP/3 (QUIC).

Deploy the appropriate virtual host template from the [angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose) repository based on your hosting model:

#### Option A: Dedicated Root Domain

For hosting under a root domain (e.g., `https://vault.example.com`), download [`vaultwarden.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/vaultwarden.conf):

```bash
DATA_ANGIE="/home/angie/data"

curl -fsSL "https://raw.githubusercontent.com/webstudiobond/angie-docker-compose/main/data/conf.d/domains/vaultwarden.conf" \
  -o "${DATA_ANGIE}/conf.d/domains/vaultwarden.conf"
```

Configuration steps:
1. In `${DATA_ANGIE}/conf.d/domains/vaultwarden.conf`, replace `vault.example` with your target domain.
2. If running multiple Vaultwarden instances, rename `vaultwarden` identifiers (upstream pool, ACME client, map/geo variables) to maintain unique names in Angie.
3. In `secrets/vw_domain.txt`, set your root origin without a trailing slash (e.g., `https://vault.example.com`).

#### Option B: Sub-Directory with Path Obfuscation

For hosting under a subpath (e.g., `https://example.com/vault/`), download [`vaultwarden_subdir.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/vaultwarden_subdir.conf):

```bash
DATA_ANGIE="/home/angie/data"

curl -fsSL "https://raw.githubusercontent.com/webstudiobond/angie-docker-compose/main/data/conf.d/domains/vaultwarden_subdir.conf" \
  -o "${DATA_ANGIE}/conf.d/domains/vaultwarden_subdir.conf"
```

Configuration steps:
1. In `${DATA_ANGIE}/conf.d/domains/vaultwarden_subdir.conf`, replace `vault.example` with your target domain.
2. Replace all occurrences of `/vault/` with your chosen custom subpath (e.g., `/<CUSTOM_SUBPATH>/`). Avoid predictable names like `/vault/` or `/bitwarden/` that are indexed in scanner dictionaries.
3. If running multiple Vaultwarden instances, rename `vaultwarden` identifiers (upstream pool, ACME client, map/geo variables) to maintain unique names in Angie.
4. In `secrets/vw_domain.txt`, set the exact matching URL (e.g., `https://example.com/<CUSTOM_SUBPATH>`). Vaultwarden constructs its internal routing and web vault asset links relative to this URL.

### Administrative Portal Access via Angie

By default, the administrative endpoint returns `404` in Angie to conceal the interface from public scanners and untrusted traffic.

To permit administrative access through the reverse proxy:
1. Open your Angie template (`vaultwarden.conf` or `vaultwarden_subdir.conf`).
2. Add your trusted IP addresses or CIDR subnets to the `geo $remote_addr $vaultwarden_admin_allowed` block (`1` = allow).
3. All unauthorized IP addresses continue to receive `404`.

The administrative location block can also be integrated with additional perimeter authentication layers, such as HTTP Basic Auth (`auth_basic`) or SSO forward authentication (`auth_request` via Authentik, Authelia, or OAuth2 proxies).

</details>

---

<details>
<summary><strong>Permissions &amp; Ownership</strong></summary>

### Host Permissions & Privilege Separation

The stack enforces strict host-level privilege separation across three ownership tiers so that even in the event of a container compromise (`vaultwarden`), the process cannot modify deployment manifests, service configurations, or secret files:

* `root:root` (0600): `docker-compose.yaml` and `.env` (accessible exclusively to host root).
* `root:vaultwarden` (0750/0440): `/home/vaultwarden` base directory (0750), `secrets/` directory (0750), and secret files (`*.txt`, `rsa_key.pem` at 0440; read-only to containers, immutable to runtime processes).
* `vaultwarden:vaultwarden` (0700): `data/` (attachments, sends, and icon cache), `postgres/` (database cluster), and `logs/` (application logs). Managed with `umask 077`: directories `0700`, files `0600`.

### One-Shot Permission Reset (Running Stack)

Use this self-contained block to apply or restore canonical ownership and permissions on an existing or migrated installation without stopping the stack:

```bash
sudo chown root:vaultwarden /home/vaultwarden
sudo chmod 0750 /home/vaultwarden

sudo chown root:root /home/vaultwarden/{docker-compose.yaml,.env}
sudo chmod 0600 /home/vaultwarden/{docker-compose.yaml,.env}

sudo chown -R root:vaultwarden /home/vaultwarden/secrets
sudo chmod 0750 /home/vaultwarden/secrets
sudo find /home/vaultwarden/secrets -type f -exec chmod 0440 {} +

sudo chown -R vaultwarden:vaultwarden /home/vaultwarden/{data,postgres,logs}
sudo chmod 0700 /home/vaultwarden/{data,postgres,logs}
sudo find /home/vaultwarden/{data,postgres} -mindepth 1 -type d -exec chmod 0700 {} +
sudo find /home/vaultwarden/{data,postgres,logs} -type f -exec chmod 0600 {} +
```

</details>

---

<details>
<summary><strong>Operation &amp; Maintenance</strong></summary>

### Verifying Service Health

Inspect container status and resource usage:

```bash
sudo docker compose -f /home/vaultwarden/docker-compose.yaml ps
sudo docker stats --no-stream vaultwarden vaultwarden-db
```

### Inspecting Runtime Logs

Follow application and database logs:

```bash
sudo docker compose -f /home/vaultwarden/docker-compose.yaml logs -f
```

### Updating the Image

Pull updated images and recreate containers:

```bash
sudo docker compose -f /home/vaultwarden/docker-compose.yaml pull
sudo docker compose -f /home/vaultwarden/docker-compose.yaml up -d
```

### Tearing Down the Stack

Stop and remove containers and networks:

```bash
sudo docker compose -f /home/vaultwarden/docker-compose.yaml down
```

</details>

---

<details>
<summary><strong>Development</strong></summary>

For local compilation, static analysis, unit testing, and Docker linting guidelines, see **[DEVELOPMENT.md](DEVELOPMENT.md)**.

</details>

---

<details>
<summary><strong>License &amp; Attribution</strong></summary>

## License & Attribution

This repository and deployment architecture are licensed under the [MIT License](LICENSE). This project is an independent containerized deployment architecture and is not affiliated with, endorsed, or sponsored by Dani García or Bitwarden Inc.

Vaultwarden is open-source software licensed under the [GNU Affero General Public License v3.0](https://github.com/dani-garcia/vaultwarden/blob/main/LICENSE.txt) and developed by [Dani García](https://github.com/dani-garcia) and contributors.

</details>
