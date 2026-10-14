#!/usr/bin/env bash
# Harden a fresh Ubuntu LTS droplet and deploy Fold with Docker Compose.
#
# Idempotent: a second run leaves users, SSH keys, .env secrets, and
# backup.env alone, and reapplies the same host configuration.
#
# This script never calls doctl and never creates droplets, firewalls,
# object-storage buckets, snapshots, or backup plans. Those are paid or account
# resources. The church creates them; the commands live in
# docs/DEPLOY-DIGITALOCEAN.md.
#
# --dry-run prints every action and changes nothing.
set -euo pipefail

DRY_RUN=0
ALLOW_BUILD=0
ALLOW_OS=0
SKIP_HARDEN=0
SKIP_DEPLOY=0
DOMAIN=""
ADMIN_USER="deploy"
SSH_PUBKEY_FILE=""
DEPLOY_DIR="/opt/fold"
SOURCE_DIR=""
IMAGE=""
TIMEZONE=""
SWAP_MB=""
SMTP_URL=""
EMAIL_FROM=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
keys=()

die() {
  printf 'provision.sh: %s\n' "$1" >&2
  exit "${2:-1}"
}

# True when a value would break a double-quoted .env line.
env_value_unsafe() {
  local value="$1"
  local quote backslash
  quote="$(printf '\042')"
  backslash="$(printf '\134')"
  [[ "$value" == *"$quote"* || "$value" == *"$backslash"* ]]
}

step() {
  if (( DRY_RUN )); then
    printf 'dry-run:'
  else
    printf '==>'
  fi
  printf ' %q' "$@"
  printf '\n'
  if (( ! DRY_RUN )); then
    "$@"
  fi
}

# write_file PATH MODE SUMMARY <<'EOF'
# SUMMARY is printed in dry-run so the plan is visible. File contents are not.
write_file() {
  local path="$1"
  local mode="$2"
  local summary="$3"
  if (( DRY_RUN )); then
    local bytes
    bytes="$(wc -c | tr -d '[:space:]')"
    printf 'dry-run: write %s mode %s (%s bytes): %s\n' "$path" "$mode" "$bytes" "$summary"
    return 0
  fi
  local dir tmp
  dir="$(dirname "$path")"
  mkdir -p "$dir"
  tmp="$(mktemp "$dir/.tmp.XXXXXX")"
  cat >"$tmp"
  chmod "$mode" "$tmp"
  mv "$tmp" "$path"
}

usage() {
  cat <<'EOF'
Usage: sudo deploy/digitalocean/provision.sh --domain groups.example.org [options]

Hardens Ubuntu 24.04 or 26.04 and deploys Fold to /opt/fold. A second run
is safe. --dry-run prints the plan and does not change the machine.

Required for a deploy (unless --skip-deploy):
  --domain NAME              Public hostname. DNS A/AAAA must point here.
  --image REF                Pull this image instead of building.
                             Example: ghcr.io/grierj/fold:0.5.0
  --allow-onbox-build        Build on this machine (docker compose up --build).
                             Off by default. A 1 GB droplet will run out of memory.

Hardening:
  --admin-user NAME          Sudo user. Password locked. Default: deploy
  --ssh-pubkey-file PATH     Public keys for that user. Default: the file that
                             already authorizes root or the invoking user.
  --timezone ZONE            timedatectl zone. Nightly backups use local 02:30.
  --swap-mb N                Create /swapfile of N MiB if it is absent.
  --allow-os                 Continue on an Ubuntu version this script does not claim.

Deploy:
  --deploy-dir PATH          Default: /opt/fold
  --source-dir PATH          Checkout to copy. Default: this repo.
  --smtp-url URL             Written only when .env is first created.
  --email-from ADDR          Written only when .env is first created.
  --skip-harden              Deploy only.
  --skip-deploy              Harden only.

--image and --allow-onbox-build are mutually exclusive. Prebuilt images are
not published yet (plan C10); until they are, a 2 GB droplet and
--allow-onbox-build is the path that matches this repo's compose file.
EOF
}

need_value() {
  [[ $# -ge 2 && -n "${2:-}" ]] || die "$1 needs a value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h)
      usage
      exit 0
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --allow-onbox-build)
      ALLOW_BUILD=1
      shift
      ;;
    --allow-os)
      ALLOW_OS=1
      shift
      ;;
    --skip-harden)
      SKIP_HARDEN=1
      shift
      ;;
    --skip-deploy)
      SKIP_DEPLOY=1
      shift
      ;;
    --domain)
      need_value "$@"
      DOMAIN="$2"
      shift 2
      ;;
    --domain=*)
      DOMAIN="${1#*=}"
      shift
      ;;
    --admin-user)
      need_value "$@"
      ADMIN_USER="$2"
      shift 2
      ;;
    --admin-user=*)
      ADMIN_USER="${1#*=}"
      shift
      ;;
    --ssh-pubkey-file)
      need_value "$@"
      SSH_PUBKEY_FILE="$2"
      shift 2
      ;;
    --ssh-pubkey-file=*)
      SSH_PUBKEY_FILE="${1#*=}"
      shift
      ;;
    --deploy-dir)
      need_value "$@"
      DEPLOY_DIR="$2"
      shift 2
      ;;
    --deploy-dir=*)
      DEPLOY_DIR="${1#*=}"
      shift
      ;;
    --source-dir)
      need_value "$@"
      SOURCE_DIR="$2"
      shift 2
      ;;
    --source-dir=*)
      SOURCE_DIR="${1#*=}"
      shift
      ;;
    --image)
      need_value "$@"
      IMAGE="$2"
      shift 2
      ;;
    --image=*)
      IMAGE="${1#*=}"
      shift
      ;;
    --timezone)
      need_value "$@"
      TIMEZONE="$2"
      shift 2
      ;;
    --timezone=*)
      TIMEZONE="${1#*=}"
      shift
      ;;
    --swap-mb)
      need_value "$@"
      SWAP_MB="$2"
      shift 2
      ;;
    --swap-mb=*)
      SWAP_MB="${1#*=}"
      shift
      ;;
    --smtp-url)
      need_value "$@"
      SMTP_URL="$2"
      shift 2
      ;;
    --smtp-url=*)
      SMTP_URL="${1#*=}"
      shift
      ;;
    --email-from)
      need_value "$@"
      EMAIL_FROM="$2"
      shift 2
      ;;
    --email-from=*)
      EMAIL_FROM="${1#*=}"
      shift
      ;;
    *)
      die "unknown option: $1 (try --help)"
      ;;
  esac
done

validate() {
  [[ -n "$DOMAIN" ]] || die "--domain is required"
  [[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$ ]] \
    || die "--domain must be a hostname such as groups.example.org"
  [[ "$ADMIN_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] \
    || die "--admin-user must be a short lowercase Linux username"
  [[ "$ADMIN_USER" != "root" ]] || die "--admin-user cannot be root"
  [[ "$DEPLOY_DIR" == /* ]] || die "--deploy-dir must be an absolute path"
  if [[ -n "$IMAGE" && "$ALLOW_BUILD" -eq 1 ]]; then
    die "pass either --image or --allow-onbox-build, not both"
  fi
  if [[ -n "$IMAGE" ]]; then
    [[ "$IMAGE" =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]*$ ]] || die "--image has characters this script will not put in a compose file"
    case "$IMAGE" in
      *:latest|*/latest)
        echo "provision.sh: warning: $IMAGE is not a pinned release tag" >&2
        ;;
    esac
  fi
  if [[ -n "$TIMEZONE" ]]; then
    [[ "$TIMEZONE" =~ ^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$ ]] || die "--timezone is not an IANA zone name"
    if [[ -d /usr/share/zoneinfo && ! -e "/usr/share/zoneinfo/$TIMEZONE" ]]; then
      die "unknown timezone: $TIMEZONE"
    fi
  fi
  if [[ -n "$SWAP_MB" ]]; then
    [[ "$SWAP_MB" =~ ^[0-9]+$ ]] || die "--swap-mb must be a number of mebibytes"
    [[ "$SWAP_MB" -le 8192 ]] || die "--swap-mb must be 8192 or less"
  fi
  if (( SKIP_HARDEN && SKIP_DEPLOY )); then
    die "pass at most one of --skip-harden and --skip-deploy"
  fi
  if [[ -n "$SMTP_URL" ]]; then
    [[ "$SMTP_URL" =~ ^smtps?://[^[:space:]]+$ ]] || die "--smtp-url must start with smtp:// or smtps://"
    if env_value_unsafe "$SMTP_URL"; then
      die "--smtp-url cannot contain a double quote or backslash"
    fi
  fi
  if [[ -n "$EMAIL_FROM" ]]; then
    case "$EMAIL_FROM" in
      *$'\n'*|*$'\r'*) die "--email-from must be one line" ;;
    esac
    if env_value_unsafe "$EMAIL_FROM"; then
      die "--email-from cannot contain a double quote or backslash"
    fi
  fi
  if (( ! SKIP_DEPLOY )); then
    if [[ -z "$IMAGE" && "$ALLOW_BUILD" -eq 0 ]]; then
      die "pass --image ghcr.io/grierj/fold:TAG to pull, or --allow-onbox-build to build here. Building on a 1–2 GB droplet can run out of memory; published images (plan C10) are not in this repo yet." 2
    fi
  fi
  if [[ -z "$SOURCE_DIR" ]]; then
    SOURCE_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
  else
    SOURCE_DIR="$(cd "$SOURCE_DIR" && pwd)"
  fi
  [[ -f "$SOURCE_DIR/docker-compose.yml" && -f "$SOURCE_DIR/Caddyfile" ]] \
    || die "source dir $SOURCE_DIR has no docker-compose.yml and Caddyfile"
  if [[ "$SOURCE_DIR" == "$DEPLOY_DIR" && -n "$IMAGE" ]]; then
    die "refusing to rewrite docker-compose.yml inside the source tree; keep the default --deploy-dir /opt/fold"
  fi
}

check_os() {
  local os_id="" os_version=""
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    os_id="${ID:-}"
    os_version="${VERSION_ID:-}"
  fi
  if [[ "$os_id" == "ubuntu" && ( "$os_version" == "24.04" || "$os_version" == "26.04" ) ]]; then
    echo "os: Ubuntu $os_version"
    return 0
  fi
  local found="${os_id:-unknown} ${os_version:-unknown}"
  if (( DRY_RUN )); then
    echo "dry-run: host is $found; a real run refuses this unless it is Ubuntu 24.04 or 26.04 or --allow-os is set"
    return 0
  fi
  if (( ALLOW_OS )); then
    echo "provision.sh: warning: continuing on $found because --allow-os was set" >&2
    return 0
  fi
  die "Ubuntu 24.04 or 26.04 required (found $found). Pass --allow-os to override."
}

require_root() {
  if [[ "$(id -u)" -eq 0 ]]; then
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: a real run must be root (sudo)"
    return 0
  fi
  die "run as root: sudo $0 ..."
}

warn_memory() {
  local mem_kb=0
  if [[ -r /proc/meminfo ]]; then
    mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
  fi
  if (( ALLOW_BUILD )) && [[ "$mem_kb" -gt 0 && "$mem_kb" -lt 1800000 ]]; then
    echo "provision.sh: warning: ${mem_kb} kB RAM. On-box builds often fail below 2 GB; pull an image when one is published." >&2
  fi
  if (( ! DRY_RUN )) && (( ALLOW_BUILD )); then
    local avail_kb
    avail_kb="$(df -Pk / | awk 'NR==2 {print $4}')"
    if [[ "$avail_kb" -lt 8000000 ]]; then
      echo "provision.sh: warning: about $(( avail_kb / 1024 )) MB free on /. The image build needs several GB." >&2
    fi
  fi
}

load_pubkey() {
  local candidate="$SSH_PUBKEY_FILE"
  if [[ -z "$candidate" && -s /root/.ssh/authorized_keys ]]; then
    candidate=/root/.ssh/authorized_keys
  fi
  if [[ -z "$candidate" && -n "${SUDO_USER:-}" && -s "/home/${SUDO_USER}/.ssh/authorized_keys" ]]; then
    candidate="/home/${SUDO_USER}/.ssh/authorized_keys"
  fi
  [[ -n "$candidate" && -f "$candidate" ]] \
    || die "no SSH public key. Pass --ssh-pubkey-file. Refusing to disable root and password logins without one."
  local line
  local key_re='^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh.com|sk-ecdsa-sha2-nistp256@openssh.com)[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$'
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    case "$line" in
      ''|'#'*) continue ;;
    esac
    [[ "$line" =~ $key_re ]] || die "refusing SSH key file $candidate: a line is not a plain OpenSSH public key"
    keys+=("$line")
  done <"$candidate"
  (( ${#keys[@]} > 0 )) || die "no SSH public key in $candidate. Refusing to lock out root."
  SSH_PUBKEY_FILE="$candidate"
  echo "ssh: ${#keys[@]} public key(s) from $SSH_PUBKEY_FILE"
}

apt_base() {
  if (( DRY_RUN )); then
    echo "dry-run: apt-get update"
    echo "dry-run: DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl openssl tzdata ufw fail2ban unattended-upgrades"
    echo "dry-run: apt-get install -y age   # when the package exists; offsite backups need it"
    return 0
  fi
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y ca-certificates curl openssl tzdata ufw fail2ban unattended-upgrades
  if apt-cache show age >/dev/null 2>&1; then
    apt-get install -y age
  else
    echo "provision.sh: age is not in apt. Offsite backups stay disabled until age is installed." >&2
  fi
}

ensure_user() {
  if id "$ADMIN_USER" >/dev/null 2>&1; then
    echo "user $ADMIN_USER already exists"
  else
    step useradd --create-home --shell /bin/bash --groups sudo "$ADMIN_USER"
  fi
  # Locked password: login is the SSH key. sudo is NOPASSWD because of that.
  step passwd -l "$ADMIN_USER"
}

install_authorized_keys() {
  local home="/home/$ADMIN_USER"
  if (( ! DRY_RUN )); then
    if id "$ADMIN_USER" >/dev/null 2>&1; then
      home="$(getent passwd "$ADMIN_USER" | cut -d: -f6)"
    fi
    [[ -n "$home" && "$home" == /* ]] || die "could not resolve home for $ADMIN_USER"
    install -d -m 700 -o "$ADMIN_USER" -g "$ADMIN_USER" "$home/.ssh"
    local auth="$home/.ssh/authorized_keys"
    if [[ ! -e "$auth" ]]; then
      install -m 600 -o "$ADMIN_USER" -g "$ADMIN_USER" /dev/null "$auth"
    fi
    local key
    for key in "${keys[@]}"; do
      if grep -qxF "$key" "$auth"; then
        continue
      fi
      printf '%s\n' "$key" >>"$auth"
    done
    chown "$ADMIN_USER:$ADMIN_USER" "$auth"
    chmod 600 "$auth"
    [[ -s "$auth" ]] || die "authorized_keys is empty; refusing to disable root login"
    echo "ssh: keys installed for $ADMIN_USER"
    return 0
  fi
  echo "dry-run: install ${#keys[@]} SSH public key(s) for $ADMIN_USER in $home/.ssh/authorized_keys mode 600"
}

write_sudoers() {
  local path="/etc/sudoers.d/fold-admin"
  write_file "$path" 440 "NOPASSWD sudo for $ADMIN_USER" <<EOF
# Managed by deploy/digitalocean/provision.sh
${ADMIN_USER} ALL=(ALL) NOPASSWD:ALL
EOF
  if (( DRY_RUN )); then
    echo "dry-run: visudo -cf $path"
    return 0
  fi
  if ! visudo -cf "$path"; then
    rm -f "$path"
    die "sudoers file rejected by visudo; removed $path"
  fi
}

harden_ssh() {
  local path="/etc/ssh/sshd_config.d/00-fold-hardening.conf"
  # 00- so this wins over 50-cloud-init.conf. sshd keeps the first value.
  write_file "$path" 644 "PermitRootLogin no, PasswordAuthentication no, AuthenticationMethods publickey" <<'EOF'
# Managed by deploy/digitalocean/provision.sh.
# Named 00- so it is read before 50-cloud-init.conf (sshd uses the first value).
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
EOF
  if (( DRY_RUN )); then
    echo "dry-run: sshd -t"
    echo "dry-run: systemctl reload ssh"
    return 0
  fi
  if ! sshd -t; then
    rm -f "$path"
    die "sshd -t failed; removed $path so a later restart cannot lock SSH out"
  fi
  systemctl reload ssh || systemctl reload sshd
}

configure_ufw() {
  # Allow SSH before the default deny, so a re-run cannot drop the current session.
  step ufw allow 22/tcp
  step ufw allow 80/tcp
  step ufw allow 443/tcp
  step ufw default deny incoming
  step ufw default allow outgoing
  step ufw --force enable
}

configure_fail2ban() {
  write_file /etc/fail2ban/jail.d/fold-sshd.local 644 "sshd jail, 5 retries, 1h ban" <<'EOF'
# Managed by deploy/digitalocean/provision.sh
[sshd]
enabled = true
port = ssh
backend = systemd
maxretry = 5
findtime = 10m
bantime = 1h
EOF
  if (( DRY_RUN )); then
    echo "dry-run: systemctl enable --now fail2ban"
    echo "dry-run: systemctl reload fail2ban"
    return 0
  fi
  systemctl enable --now fail2ban
  systemctl reload fail2ban || systemctl restart fail2ban
}

configure_unattended() {
  write_file /etc/apt/apt.conf.d/20auto-upgrades 644 "daily package lists and unattended upgrades" <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
  write_file /etc/apt/apt.conf.d/52fold-unattended 644 "security upgrades, no automatic reboot" <<'EOF'
// Managed by deploy/digitalocean/provision.sh.
// No automatic reboot: a church event should not lose the site at 4am.
// Reboot when a kernel update is waiting; see docs/DEPLOY-DIGITALOCEAN.md.
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
EOF
  if (( DRY_RUN )); then
    echo "dry-run: dpkg-reconfigure -f noninteractive unattended-upgrades"
    echo "dry-run: systemctl enable --now unattended-upgrades"
    return 0
  fi
  dpkg-reconfigure -f noninteractive unattended-upgrades
  systemctl enable --now unattended-upgrades \
    || echo "provision.sh: unattended-upgrades service did not enable; apt periodic config still applies" >&2
}

configure_timezone() {
  [[ -n "$TIMEZONE" ]] || return 0
  step timedatectl set-timezone "$TIMEZONE"
}

configure_swap() {
  [[ -n "$SWAP_MB" && "$SWAP_MB" -gt 0 ]] || return 0
  if [[ -e /swapfile ]]; then
    echo "/swapfile already exists; leaving it"
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: fallocate -l ${SWAP_MB}M /swapfile"
    echo "dry-run: chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile"
    echo "dry-run: append /swapfile to /etc/fstab if missing"
    return 0
  fi
  if ! fallocate -l "${SWAP_MB}M" /swapfile; then
    dd if=/dev/zero of=/swapfile bs=1M count="$SWAP_MB" status=none
  fi
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  if ! grep -q '^/swapfile[[:space:]]' /etc/fstab; then
    printf '%s\n' '/swapfile none swap sw 0 0' >>/etc/fstab
  fi
}

install_docker() {
  if docker compose version >/dev/null 2>&1; then
    echo "docker compose already installed"
  elif (( ! DRY_RUN )) && dpkg -s docker.io >/dev/null 2>&1; then
    die "Ubuntu docker.io is installed. Start from a fresh droplet, or remove docker.io before installing Docker Engine."
  elif (( DRY_RUN )); then
    echo "dry-run: install -m 0755 -d /etc/apt/keyrings"
    echo "dry-run: curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc"
    echo "dry-run: write /etc/apt/sources.list.d/docker.sources for this Ubuntu codename"
    echo "dry-run: apt-get update"
    echo "dry-run: DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin"
    echo "dry-run: systemctl enable --now docker"
  else
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
    local codename arch
    # shellcheck disable=SC1091
    . /etc/os-release
    codename="${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}"
    [[ -n "$codename" ]] || die "could not read the Ubuntu codename from /etc/os-release"
    arch="$(dpkg --print-architecture)"
    cat >/etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${codename}
Components: stable
Architectures: ${arch}
Signed-By: /etc/apt/keyrings/docker.asc
EOF
    apt-get update
    apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    systemctl enable --now docker
  fi
  if (( DRY_RUN )); then
    echo "dry-run: usermod -aG docker $ADMIN_USER"
    return 0
  fi
  if getent group docker >/dev/null && id "$ADMIN_USER" >/dev/null 2>&1; then
    usermod -aG docker "$ADMIN_USER"
  fi
}

copy_tree() {
  local src="$1"
  local dest="$2"
  if [[ "$src" == "$dest" ]]; then
    echo "source and deploy dir are the same; not copying the tree"
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: copy $src to $dest (excluding .git, node_modules, .env, backup.env)"
    return 0
  fi
  local stage
  stage="$(mktemp -d)"
  if ! tar -C "$src" \
    --exclude .git \
    --exclude node_modules \
    --exclude .env \
    --exclude backup.env \
    --exclude data \
    -cf - . | tar -C "$stage" -xf -; then
    rm -rf "$stage"
    die "copy from $src failed"
  fi
  # Drop secrets the checkout might have carried. Never delete $dest/.env:
  # a re-run must keep the live SESSION_SECRET and DB_PASSWORD.
  rm -f "$stage/.env" "$stage/backup.env"
  mkdir -p "$dest"
  if ! tar -C "$stage" -cf - . | tar -C "$dest" -xf -; then
    rm -rf "$stage"
    die "copy into $dest failed"
  fi
  rm -rf "$stage"
}

write_image_compose() {
  local src="$SOURCE_DIR/docker-compose.yml"
  local dest="$DEPLOY_DIR/docker-compose.yml"
  if (( DRY_RUN )); then
    echo "dry-run: write $dest from $src with app image $IMAGE (build: removed)"
    return 0
  fi
  local tmp
  tmp="$(mktemp)"
  if ! awk -v image="$IMAGE" '
    BEGIN { replaced = 0 }
    {
      if (!replaced && $0 == "    build: .") {
        print "    image: " image " # fold-app-image"
        replaced = 1
        next
      }
      print
    }
    END { if (!replaced) exit 3 }
  ' "$src" >"$tmp"; then
    rm -f "$tmp"
    die "docker-compose.yml has no '    build: .' line. Refusing to guess how to switch it to a pulled image."
  fi
  if grep -n '^    build:' "$tmp" >/dev/null; then
    rm -f "$tmp"
    die "rewritten compose still has an app build key; refusing to build on the droplet"
  fi
  mkdir -p "$DEPLOY_DIR"
  mv "$tmp" "$dest"
  chmod 644 "$dest"
}

install_runtime_files() {
  if (( DRY_RUN )); then
    echo "dry-run: mkdir -p $DEPLOY_DIR mode 755"
  else
    mkdir -p "$DEPLOY_DIR"
    chmod 755 "$DEPLOY_DIR"
  fi
  if [[ -n "$IMAGE" ]]; then
    if (( DRY_RUN )); then
      echo "dry-run: copy Caddyfile and .env.example to $DEPLOY_DIR"
    else
      install -m 644 "$SOURCE_DIR/Caddyfile" "$DEPLOY_DIR/Caddyfile"
      install -m 644 "$SOURCE_DIR/.env.example" "$DEPLOY_DIR/.env.example"
    fi
    write_image_compose
  else
    copy_tree "$SOURCE_DIR" "$DEPLOY_DIR"
  fi
  if (( DRY_RUN )); then
    echo "dry-run: install $SCRIPT_DIR/backup.sh to $DEPLOY_DIR/backup.sh mode 755"
    echo "dry-run: install $SCRIPT_DIR/backup.env.example to $DEPLOY_DIR/backup.env.example mode 644"
    return 0
  fi
  install -m 755 "$SCRIPT_DIR/backup.sh" "$DEPLOY_DIR/backup.sh"
  install -m 644 "$SCRIPT_DIR/backup.env.example" "$DEPLOY_DIR/backup.env.example"
  if [[ -f "$DEPLOY_DIR/backup.env" ]]; then
    chmod 600 "$DEPLOY_DIR/backup.env"
  fi
}

env_file_domain() {
  local file="$1"
  local line key val
  [[ -f "$file" ]] || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      ''|'#'*) continue ;;
    esac
    key="${line%%=*}"
    val="${line#*=}"
    if [[ "$val" == \"*\" && "$val" == *\" ]]; then
      val="${val:1:${#val}-2}"
    fi
    if [[ "$key" == "DOMAIN" ]]; then
      printf '%s' "$val"
      return 0
    fi
  done <"$file"
}

write_env() {
  local env_file="$DEPLOY_DIR/.env"
  if [[ -f "$env_file" ]]; then
    local existing
    existing="$(env_file_domain "$env_file")"
    if [[ -n "$existing" && "$existing" != "$DOMAIN" ]]; then
      die "$env_file DOMAIN is $existing; this run passed --domain $DOMAIN. Edit .env yourself if the name should change. Secrets were left untouched."
    fi
    echo "$env_file already exists; leaving secrets unchanged"
    if (( ! DRY_RUN )); then
      chmod 600 "$env_file"
    else
      echo "dry-run: chmod 600 $env_file"
    fi
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: write $env_file mode 600 (DOMAIN=$DOMAIN; SESSION_SECRET and DB_PASSWORD generated; values not shown)"
    return 0
  fi
  local session dbpass
  session="$(openssl rand -base64 48 | tr -d '\n')"
  dbpass="$(openssl rand -hex 24)"
  [[ "${#session}" -ge 32 ]] || die "generated SESSION_SECRET is shorter than 32 characters"
  [[ "${#dbpass}" -ge 16 ]] || die "generated DB_PASSWORD is too short"
  umask 077
  {
    echo "# Generated by deploy/digitalocean/provision.sh. Mode 600. Do not commit."
    echo "COMPOSE_PROJECT_NAME=fold"
    printf 'DOMAIN="%s"\n' "$DOMAIN"
    printf 'SESSION_SECRET="%s"\n' "$session"
    printf 'DB_PASSWORD="%s"\n' "$dbpass"
    if [[ -n "$SMTP_URL" ]]; then
      printf 'FOLD_SMTP_URL="%s"\n' "$SMTP_URL"
    else
      echo "# Email is required before inviting people. SMS is optional and stays unset."
      echo "# FOLD_SMTP_URL=smtp://user:pass@smtp.example.org:587"
    fi
    if [[ -n "$EMAIL_FROM" ]]; then
      printf 'FOLD_EMAIL_FROM="%s"\n' "$EMAIL_FROM"
    else
      echo "# FOLD_EMAIL_FROM=Fold <noreply@example.org>"
    fi
  } >"$env_file"
  chmod 600 "$env_file"
  if [[ -n "$SMTP_URL" ]]; then
    echo "provision.sh: SMTP settings were written to $env_file. They may also be in your shell history." >&2
  fi
}

install_healthcheck() {
  local path="$DEPLOY_DIR/healthcheck.sh"
  write_file "$path" 755 "curl https://$DOMAIN/healthz; log on failure" <<EOF
#!/bin/bash
# Managed by deploy/digitalocean/provision.sh
set -euo pipefail
if ! curl -fsS --max-time 15 "https://${DOMAIN}/healthz" >/dev/null; then
  logger -t fold-health "https://${DOMAIN}/healthz failed"
fi
EOF
  write_file /etc/cron.d/fold-health 644 "healthz every 5 minutes" <<EOF
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin
*/5 * * * * root ${path}
EOF
}

install_backup_cron() {
  write_file /etc/cron.d/fold-backup 644 "pg_dump at 02:30 local time" <<EOF
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin
# 02:30 in the droplet timezone (timedatectl). Local dumps plus optional offsite.
30 2 * * * root ${DEPLOY_DIR}/backup.sh >> /var/log/fold-backup.log 2>&1
EOF
  write_file /etc/logrotate.d/fold-backup 644 "weekly rotate of the backup log" <<'EOF'
/var/log/fold-backup.log {
  weekly
  rotate 8
  compress
  missingok
  notifempty
  copytruncate
}
EOF
}

deploy_stack() {
  local -a cmd
  cmd=(docker compose --project-directory "$DEPLOY_DIR")
  if [[ -n "$IMAGE" ]]; then
    step "${cmd[@]}" pull
    step "${cmd[@]}" up -d
  else
    step "${cmd[@]}" up --build -d
  fi
}

wait_healthy() {
  if (( DRY_RUN )); then
    echo "dry-run: wait until the app container is healthy"
    echo "dry-run: remind the operator to open https://$DOMAIN/ after DNS points here"
    return 0
  fi
  local i cid status
  i=0
  while [[ "$i" -lt 36 ]]; do
    i=$((i + 1))
    cid="$(docker compose --project-directory "$DEPLOY_DIR" ps -q app || true)"
    if [[ -n "$cid" ]]; then
      status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid")"
      if [[ "$status" == "healthy" ]]; then
        echo "app container is healthy"
        return 0
      fi
    fi
    sleep 5
  done
  echo "provision.sh: app container did not become healthy. Logs: docker compose --project-directory $DEPLOY_DIR logs app --tail 200" >&2
  return 1
}

summarize() {
  cat <<EOF

Fold deploy plan for https://${DOMAIN}/
  admin user: ${ADMIN_USER} (SSH key, locked password, sudo)
  files:      ${DEPLOY_DIR}
  database:   Postgres 16 in the compose db service (user church, database fold)
EOF
  if [[ -n "$IMAGE" ]]; then
    echo "  app image: ${IMAGE}"
  elif (( ALLOW_BUILD )); then
    echo "  app image: built on this machine (--allow-onbox-build)"
  fi
  if (( DRY_RUN )); then
    echo "  dry-run:    no files were written and no packages were installed"
  else
    cat <<EOF
  next:       confirm a second terminal can 'ssh ${ADMIN_USER}@${DOMAIN}' before closing root
              point DNS at this host if you have not, then open https://${DOMAIN}/
              the first visit is the setup wizard
              put SMTP in ${DEPLOY_DIR}/.env before inviting people (SMS stays unset)
              copy ${DEPLOY_DIR}/backup.env.example to backup.env to ship dumps off the box
EOF
  fi
}

main() {
  if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
  fi
  validate
  check_os
  require_root
  warn_memory
  if (( ! SKIP_HARDEN )); then
    load_pubkey
    apt_base
    ensure_user
    install_authorized_keys
    write_sudoers
    harden_ssh
    configure_ufw
    configure_fail2ban
    configure_unattended
    configure_timezone
    configure_swap
  fi
  if (( ! SKIP_DEPLOY )); then
    install_docker
    install_runtime_files
    write_env
    install_healthcheck
    install_backup_cron
    deploy_stack
    wait_healthy
  fi
  summarize
}

main "$@"
