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
TOOL=provision.sh

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
TARGET_LEVEL=0
HOST_FLOOR=0
PIN_SAVE=""
PIN_SAVE_READY=0
PIN_ONLY=0
RECOVERY_NONCE=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$SCRIPT_DIR/checks.sh"
keys=()

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
  --image REF                Pull this pinned release instead of building. The tag
                             becomes FOLD_VERSION in .env.
                             Example: ghcr.io/grierj/fold:0.5.0
                             :latest is refused.
  --allow-onbox-build        Build on this machine: also copies
                             docker-compose.build.yml and sets COMPOSE_FILE in
                             .env (docker compose up --build). Off by default.
                             A 1 GB droplet will run out of memory.

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
  --pin-only --recovery-nonce N  Internal restore child, inherited operations lock only.
  --skip-harden              Deploy only.
  --skip-deploy              Harden only.

--image and --allow-onbox-build are mutually exclusive. The shipped compose
file pulls ghcr.io/grierj/fold:${FOLD_VERSION} (plan C10), so a 1 GB droplet
with swap can run it. Until the first release is published there is nothing
to pull: use a 2 GB droplet and --allow-onbox-build.
EOF
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
    --pin-only) PIN_ONLY=1; shift ;;
    --recovery-nonce) take_value RECOVERY_NONCE "$@"; shift "$OPT_SHIFT" ;;
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
    --domain|--domain=*) take_value DOMAIN "$@"; shift "$OPT_SHIFT" ;;
    --admin-user|--admin-user=*) take_value ADMIN_USER "$@"; shift "$OPT_SHIFT" ;;
    --ssh-pubkey-file|--ssh-pubkey-file=*) take_value SSH_PUBKEY_FILE "$@"; shift "$OPT_SHIFT" ;;
    --deploy-dir|--deploy-dir=*) take_value DEPLOY_DIR "$@"; shift "$OPT_SHIFT" ;;
    --source-dir|--source-dir=*) take_value SOURCE_DIR "$@"; shift "$OPT_SHIFT" ;;
    --image|--image=*) take_value IMAGE "$@"; shift "$OPT_SHIFT" ;;
    --timezone|--timezone=*) take_value TIMEZONE "$@"; shift "$OPT_SHIFT" ;;
    --swap-mb|--swap-mb=*) take_value SWAP_MB "$@"; shift "$OPT_SHIFT" ;;
    --smtp-url|--smtp-url=*) take_value SMTP_URL "$@"; shift "$OPT_SHIFT" ;;
    --email-from|--email-from=*) take_value EMAIL_FROM "$@"; shift "$OPT_SHIFT" ;;
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
    assert_pinned_image "$IMAGE" || exit 1
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
      die "pass --image ghcr.io/grierj/fold:TAG to pull a published release, or --allow-onbox-build to build here. Building on a 1–2 GB droplet can run out of memory; use it only until the first release is published." 2
    fi
  fi
  if (( ALLOW_BUILD )); then
    # COMPOSE_FILE in .env lists absolute paths joined by colons.
    [[ "$DEPLOY_DIR" =~ ^/[A-Za-z0-9._/-]+$ ]] \
      || die "--deploy-dir may use only letters, digits, dot, dash, underscore and slash with --allow-onbox-build"
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
    echo "dry-run: DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl dnsutils jq openssl tzdata ufw fail2ban unattended-upgrades"
    echo "dry-run: apt-get install -y age   # when the package exists; offsite backups need it"
    return 0
  fi
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y ca-certificates curl dnsutils jq openssl tzdata ufw fail2ban unattended-upgrades
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
  # passwd -S prints "user L ..." once the password is locked. A second
  # passwd -l rewrites the shadow date, so skip it when already locked.
  local locked
  locked="$(passwd -S "$ADMIN_USER" 2>/dev/null || true)"
  case "$locked" in
    *" L "*) echo "password for $ADMIN_USER already locked" ;;
    *) step passwd -l "$ADMIN_USER" ;;
  esac
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
    echo "dry-run: sshd -T must show permitrootlogin no and passwordauthentication no"
    return 0
  fi
  if ! sshd -t; then
    rm -f "$path"
    die "sshd -t failed; removed $path so a later restart cannot lock SSH out"
  fi
  systemctl reload ssh || systemctl reload sshd
  assert_ssh_locked || exit 1
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
    if id -nG "$ADMIN_USER" | tr ' ' '\n' | grep -qx docker; then
      echo "user $ADMIN_USER already in group docker"
    else
      usermod -aG docker "$ADMIN_USER"
    fi
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
    --exclude backup.lock --exclude ops.lock --exclude ops.owner --exclude recovery-policy-floor \
    --exclude deploy-tools --exclude recovery-tools --exclude recovery.lock \
    --exclude deploy/digitalocean --exclude '.recovery-tools.*' --exclude '.old-recovery-tools.*' --exclude '.deploy-tools.*' --exclude '.old-deploy-tools.*' --exclude .deploy-drill \
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

# The shipped docker-compose.yml pulls ${FOLD_IMAGE_REPO}:${FOLD_VERSION} and
# has no build key (plan C10). A normal deploy installs it unchanged and
# pins the release through FOLD_VERSION in .env (ensure_release_pin). Only an
# --image from another repository (a fork or a hotfix build) rewrites the
# image line, to an explicit pin that upgrade.sh reads back via the marker.
write_image_compose() {
  local src="$SOURCE_DIR/docker-compose.yml"
  local dest="$DEPLOY_DIR/docker-compose.yml"
  local pull_line='^    image: ghcr\.io/grierj/fold:\$\{FOLD_VERSION'
  grep -Eq "$pull_line" "$src" \
    || die "docker-compose.yml has no 'image: ${FOLD_IMAGE_REPO}:\${FOLD_VERSION...}' app line. Refusing to guess how to pull a pinned image."
  if grep -q '^    build:' "$src"; then
    die "docker-compose.yml has an app build key; the build belongs in docker-compose.build.yml"
  fi
  if [[ -z "$IMAGE" || "$IMAGE" == "${FOLD_IMAGE_REPO}:"* ]]; then
    if (( DRY_RUN )); then
      echo "dry-run: install $src as $dest unchanged (app pulls ${FOLD_IMAGE_REPO}:\${FOLD_VERSION} from .env)"
      return 0
    fi
    mkdir -p "$DEPLOY_DIR"
    install -m 644 "$src" "$dest"
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: write $dest from $src with app image $IMAGE pinned explicitly"
    return 0
  fi
  local tmp
  tmp="$(mktemp)"
  if ! IMAGE_REF="$IMAGE" PULL_LINE="$pull_line" awk '
    BEGIN { replaced = 0 }
    {
      if (!replaced && $0 ~ ENVIRON["PULL_LINE"]) {
        print "    image: " ENVIRON["IMAGE_REF"] " # fold-app-image"
        replaced = 1
        next
      }
      print
    }
    END { if (!replaced) exit 3 }
  ' "$src" >"$tmp"; then
    rm -f "$tmp"
    die "could not pin the app image in docker-compose.yml"
  fi
  mkdir -p "$DEPLOY_DIR"
  mv "$tmp" "$dest"
  chmod 644 "$dest"
}

# install_runtime_files: plain deploys copy only docker-compose.yml, the
# Caddyfile and .env.example. --allow-onbox-build copies the whole tree, which
# carries docker-compose.build.yml and the Dockerfile the build needs.
install_runtime_files() {
  if (( DRY_RUN )); then
    echo "dry-run: mkdir -p $DEPLOY_DIR mode 755"
  else
    mkdir -p "$DEPLOY_DIR"
    chmod 755 "$DEPLOY_DIR"
    # A running backup reads these tools/config files. Serialize their copy
    # with it using the same lock order as restore, without replacing the inode.
    backup_coord_acquire "$DEPLOY_DIR" 120 provision || exit $?
  fi
  if (( ALLOW_BUILD )); then
    copy_tree "$SOURCE_DIR" "$DEPLOY_DIR"
  else
    if (( DRY_RUN )); then
      echo "dry-run: copy Caddyfile and .env.example to $DEPLOY_DIR; remove any docker-compose.build.yml"
    else
      install -m 644 "$SOURCE_DIR/Caddyfile" "$DEPLOY_DIR/Caddyfile"
      install -m 644 "$SOURCE_DIR/.env.example" "$DEPLOY_DIR/.env.example"
      # A pulled deploy has no build override to fall back on by accident.
      rm -f "$DEPLOY_DIR/docker-compose.build.yml"
    fi
    write_image_compose
  fi
  if (( DRY_RUN )); then
    echo "dry-run: install backup.sh smoke.sh email-dns.sh upgrade.sh provision.sh checks.sh restore.sh compose.recovery.yml backup.env.example into $DEPLOY_DIR"
    echo "dry-run: install $SCRIPT_DIR/backup.env.example to $DEPLOY_DIR/backup.env.example mode 644"
    echo "dry-run: initialize $DEPLOY_DIR/backup.lock mode 600 without replacing its inode"
    echo "dry-run: install $SCRIPT_DIR/deploy-tools-version to $DEPLOY_DIR/deploy-tools-version mode 644"
    return 0
  fi
  local tool mode
  while IFS= read -r tool; do
    mode=755
    [[ "$tool" == *.sh && "$tool" != checks.sh ]] || mode=644
    if [[ "$SCRIPT_DIR/$tool" -ef "$DEPLOY_DIR/$tool" ]]; then
      chmod "$mode" "$DEPLOY_DIR/$tool"
    else
      install -m "$mode" "$SCRIPT_DIR/$tool" "$DEPLOY_DIR/$tool"
    fi
  done < <(runtime_tools)
  if [[ -f "$DEPLOY_DIR/backup.env" ]]; then
    chmod 600 "$DEPLOY_DIR/backup.env"
    assert_env_mode "$DEPLOY_DIR/backup.env" || exit 1
  fi
  backup_coord_release || exit 1
}

# set_env_value FILE KEY VALUE: set KEY=VALUE, replacing the first uncommented
# KEY= line or appending. Touches the file only when the value changes, so a
# second run of provision.sh leaves .env byte for byte the same. Values here
# are release labels and absolute paths: no quotes, spaces or backslashes.
set_env_value() {
  local file="$1" key="$2" value="$3" current tmp
  current="$(env_value "$file" "$key" || true)"
  if [[ "$current" == "$value" ]]; then
    return 0
  fi
  if (( DRY_RUN )); then
    echo "dry-run: set ${key}=${value} in ${file}"
    return 0
  fi
  tmp="$(mktemp "$(dirname "$file")/.tmp.XXXXXX")"
  ENV_KEY="$key" ENV_VALUE="$value" awk '
    BEGIN { key = ENVIRON["ENV_KEY"]; done = 0 }
    index($0, key "=") == 1 { if (!done) print key "=" ENVIRON["ENV_VALUE"]; done = 1; next }
    { print }
    END { if (!done) print key "=" ENVIRON["ENV_VALUE"] }
  ' "$file" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

unset_env_value() {
  local file="$1" key="$2" tmp
  [[ -n "$(env_value "$file" "$key" || true)" ]] || return 0
  if (( DRY_RUN )); then
    echo "dry-run: remove ${key} from ${file}"
    return 0
  fi
  tmp="$(mktemp "$(dirname "$file")/.tmp.XXXXXX")"
  ENV_KEY="$key" awk 'index($0, ENVIRON["ENV_KEY"] "=") != 1' "$file" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

# ensure_release_pin: FOLD_VERSION in .env is what Compose pulls (plan C10).
# --image sets it from the tag; --allow-onbox-build sets a local label and
# COMPOSE_FILE so plain `docker compose` commands include the build override.
ensure_release_pin() {
  local env_file="$DEPLOY_DIR/.env" version existing compose_file
  if [[ -n "$IMAGE" ]]; then
    version="${IMAGE##*:}"
    assert_pinned_version "$version" || exit 1
    echo "release: pulling ${IMAGE} (FOLD_VERSION=${version})"
    if (( DRY_RUN )) && [[ ! -f "$env_file" ]]; then
      echo "dry-run: set FOLD_VERSION=${version} in ${env_file}"
      return 0
    fi
    set_env_value "$env_file" FOLD_VERSION "$version"
    compose_file="$(env_value "$env_file" COMPOSE_FILE || true)"
    if [[ "$compose_file" == *docker-compose.build.yml* ]]; then
      unset_env_value "$env_file" COMPOSE_FILE
    fi
    return 0
  fi
  if (( DRY_RUN )) && [[ ! -f "$env_file" ]]; then
    echo "dry-run: set FOLD_VERSION=dev and COMPOSE_FILE=${DEPLOY_DIR}/docker-compose.yml:${DEPLOY_DIR}/docker-compose.build.yml in ${env_file}"
    return 0
  fi
  compose_file="$(env_value "$env_file" COMPOSE_FILE || true)"
  existing="$(env_value "$env_file" FOLD_VERSION || true)"
  if [[ "$compose_file" != *docker-compose.build.yml* || -z "$existing" || "$existing" == latest ]]; then
    # Switching from a pulled release: never label a local build with the
    # release number it is not.
    set_env_value "$env_file" FOLD_VERSION dev
  fi
  set_env_value "$env_file" COMPOSE_FILE "${DEPLOY_DIR}/docker-compose.yml:${DEPLOY_DIR}/docker-compose.build.yml"
  echo "release: building on this machine (docker-compose.build.yml, COMPOSE_FILE in .env)"
}

write_env() {
  local env_file="$DEPLOY_DIR/.env"
  if [[ -f "$env_file" ]]; then
    local existing
    existing="$(env_value "$env_file" DOMAIN || true)"
    if [[ -n "$existing" && "$existing" != "$DOMAIN" ]]; then
      die "$env_file DOMAIN is $existing; this run passed --domain $DOMAIN. Edit .env yourself if the name should change. Secrets were left untouched."
    fi
    echo "$env_file already exists; leaving secrets unchanged"
    if (( ! DRY_RUN )); then
      chmod 600 "$env_file"
      assert_env_mode "$env_file" || exit 1
      assert_env_untracked "$SOURCE_DIR" "$DEPLOY_DIR" || exit 1
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
  assert_env_mode "$env_file" || exit 1
  assert_env_untracked "$SOURCE_DIR" "$DEPLOY_DIR" || exit 1
  if [[ -n "$SMTP_URL" ]]; then
    echo "provision.sh: SMTP settings were written to $env_file. They may also be in your shell history." >&2
  fi
}

install_smoke_cron() {
  write_file /etc/cron.d/fold-health 644 "smoke.sh every 5 minutes" <<EOF
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin
*/5 * * * * root ${DEPLOY_DIR}/smoke.sh --deploy-dir ${DEPLOY_DIR} --timeout 60 || logger -t fold-health "smoke failed for https://${DOMAIN}/"
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

# Refuse before installing files or changing a pin. Reading an image's version
# runs an ephemeral container only, never a Compose service or a database.
preflight_recovery_level() {
  if (( DRY_RUN )); then
    local mode=image
    (( ! ALLOW_BUILD )) || mode=build
    dry_run_recovery_check "$DEPLOY_DIR" "$mode" "$SOURCE_DIR" HEAD || exit 1
    return 0
  fi
  HOST_FLOOR="$(host_recovery_floor "$DEPLOY_DIR")"
  if (( ALLOW_BUILD )); then
    TARGET_LEVEL="$(commit_recovery_level "$SOURCE_DIR" HEAD)"
  else
    docker pull "$IMAGE"
    local checked_image
    checked_image="$(docker image inspect --format '{{.Id}}' "$IMAGE")"
    pin_image_matches_expected "$checked_image"
    TARGET_LEVEL="$(image_recovery_level "$checked_image")"
  fi
  require_recovery_level "$TARGET_LEVEL" "$HOST_FLOOR" || exit 1
}

# The restore lock already checked the target immutable id. A tag that moves
# at either pin pull is a failed pin, never authority to choose another image.
pin_image_matches_expected() {
  (( PIN_ONLY )) && [[ -n "$IMAGE" ]] || return 0
  local expected
  expected="$(jq -er .pinExpected.imageId "$DEPLOY_DIR/recovery.lock")" || die 'missing checked recovery image id'
  [[ "$expected" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'invalid checked recovery image id'
  [[ "$1" == "$expected" ]] || die "pin image $1 differs from checked recovery image $expected; restoring saved pin; no services started"
}

# shellcheck disable=SC2329 # Invoked by the saved-pin EXIT trap.
finish_saved_pin() {
  local rc=$?
  trap - EXIT
  if (( PIN_ONLY && PIN_SAVE_READY && rc!=0 )); then
    if ! restore_release_pin; then
      echo "provision.sh: saved pin restoration failed; keep the site closed and inspect restore.sh --status" >&2
      rc=1
    fi
  fi
  [[ -z "$PIN_SAVE" ]] || rm -rf -- "$PIN_SAVE"
  exit "$rc"
}

save_release_pin() {
  (( ! DRY_RUN )) || return 0
  if (( PIN_ONLY )); then remove_stale_pin_saves "$DEPLOY_DIR" || die 'cannot remove stale pin snapshots'; fi
  PIN_SAVE="$(mktemp -d "$DEPLOY_DIR/.pin-save.XXXXXX")"
  trap finish_saved_pin EXIT
  local file
  for file in .env docker-compose.yml docker-compose.build.yml; do
    [[ ! -f "$DEPLOY_DIR/$file" ]] || cp -p "$DEPLOY_DIR/$file" "$PIN_SAVE/$file"
  done
  if (( PIN_ONLY )); then
    local ref id
    ref="$(compose_in "$DEPLOY_DIR" config --format json | jq -er .services.app.image)" || die 'cannot read the current image reference; repair the Compose pin and retry'
    id="$(docker image inspect --format '{{.Id}}' "$ref")" || die "current image $ref is missing locally; pull or build the pinned image and retry"
    [[ "$id" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'cannot save the current image id before pinning'
    printf '%s\n' "$ref" >"$PIN_SAVE/image-ref"
    printf '%s\n' "$id" >"$PIN_SAVE/image-id"
  fi
  PIN_SAVE_READY=1
}

restore_release_pin() {
  local file
  for file in .env docker-compose.yml docker-compose.build.yml; do
    if [[ -f "$PIN_SAVE/$file" ]]; then
      cp -p "$PIN_SAVE/$file" "$DEPLOY_DIR/$file" || return 1
    else
      rm -f "$DEPLOY_DIR/$file" || return 1
    fi
  done
  if (( PIN_ONLY )); then
    local ref id observed
    ref="$(cat "$PIN_SAVE/image-ref")" || return 1
    id="$(cat "$PIN_SAVE/image-id")" || return 1
    observed="$(docker image inspect --format '{{.Id}}' "$ref")" || return 1
    if [[ "$observed" != "$id" ]]; then docker image tag "$id" "$ref" || return 1; fi
  fi
}

deploy_stack() {
  local -a cmd=(docker compose --project-directory "$DEPLOY_DIR")
  local config image actual
  if [[ -n "$IMAGE" ]]; then
    if (( PIN_ONLY )); then step "${cmd[@]}" pull app; else step "${cmd[@]}" pull; fi
  else
    (( ! DRY_RUN )) || echo "dry-run: build app before up (previously up --build)"
    step "${cmd[@]}" build app
  fi
  if (( ! DRY_RUN )); then
    config="$("${cmd[@]}" config --format json)" || { restore_release_pin; return 1; }
    image="$(jq -er '.services.app.image' <<<"$config")" || { restore_release_pin; return 1; }
    image="$(docker image inspect --format '{{.Id}}' "$image")" || { restore_release_pin; return 1; }
    pin_image_matches_expected "$image"
    actual="$(image_recovery_level "$image")" || { restore_release_pin; return 1; }
    if [[ "$actual" != "$TARGET_LEVEL" ]]; then
      restore_release_pin
      die "resulting image recovery level ${actual} differs from checked target ${TARGET_LEVEL}; restored previous pin; no services started"
    fi
    require_recovery_level "$actual" "$HOST_FLOOR" || { restore_release_pin; return 1; }
    (( ! PIN_ONLY )) || return 0
    deploy_drill_pause "$DEPLOY_DIR" before-up
  fi
  (( ! PIN_ONLY )) || return 0
  step "${cmd[@]}" up -d
}

verify_before_up() {
  if (( DRY_RUN )); then
    echo "dry-run: check compose does not publish db/app, FOLD_TRUSTED_PROXIES matches the subnet, and FOLD_VERSION is not latest"
    return 0
  fi
  assert_deploy_pinned "$DEPLOY_DIR" || exit 1
  assert_compose_private "$DEPLOY_DIR/docker-compose.yml" || exit 1
  assert_trusted_proxy "$DEPLOY_DIR/docker-compose.yml" "$DEPLOY_DIR/.env" || exit 1
}

run_email_dns() {
  if (( DRY_RUN )); then
    echo "dry-run: $DEPLOY_DIR/email-dns.sh --domain $DOMAIN (no API calls; skipped when POSTMARK_ACCOUNT_TOKEN is unset)"
    return 0
  fi
  "$DEPLOY_DIR/email-dns.sh" --domain "$DOMAIN" --deploy-dir "$DEPLOY_DIR"
}

run_smoke() {
  if (( DRY_RUN )); then
    echo "dry-run: $DEPLOY_DIR/smoke.sh --deploy-dir $DEPLOY_DIR --domain $DOMAIN"
    echo "dry-run: smoke waits until db and app are healthy, curls https://$DOMAIN/healthz, checks the TLS certificate, and fails if 5432 or 8080 is reachable"
    return 0
  fi
  "$DEPLOY_DIR/smoke.sh" --deploy-dir "$DEPLOY_DIR" --domain "$DOMAIN" --source-dir "$SOURCE_DIR"
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
    echo "  app image: built on this machine (--allow-onbox-build, docker-compose.build.yml)"
  fi
  if (( DRY_RUN )); then
    echo "  dry-run:    no files were written and no packages were installed"
  else
    cat <<EOF
  next:       confirm a second terminal can 'ssh ${ADMIN_USER}@${DOMAIN}' before closing root
              point DNS at this host if you have not, then open https://${DOMAIN}/
              enter the setup code from docker compose logs app to create the first admin
              smoke.sh already requested https://${DOMAIN}/healthz
              put SMTP in ${DEPLOY_DIR}/.env before inviting people, or re-run with
              POSTMARK_ACCOUNT_TOKEN set so email-dns.sh can create the domain (SMS stays unset)
              copy ${DEPLOY_DIR}/backup.env.example to backup.env to ship dumps off the box
EOF
  fi
}

main() {
  if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
  fi
  # Acquire before validate reads the source tree, .env or any Docker state.
  [[ "$DEPLOY_DIR" == /* ]] || die "--deploy-dir must be an absolute path"
  if (( ! DRY_RUN )); then
    require_root
    mkdir -p "$DEPLOY_DIR"
    DEPLOY_DIR="$(cd "$DEPLOY_DIR" && pwd -P)"
  fi
  local inherited=0
  if (( ! DRY_RUN )); then
    if inherited_ops_lock "$DEPLOY_DIR"; then inherited=1; fi
    # Evaluate recovery authorization under the same lock as the rest of deploy.
    ops_lock_acquire "$DEPLOY_DIR" provision || exit $?
  fi
  validate_deploy_drill "$DEPLOY_DIR" || exit 1
  if (( ! DRY_RUN )); then
    deploy_drill_pause "$DEPLOY_DIR" after-lock
  fi
  if [[ -e "$DEPLOY_DIR/recovery.lock" || -L "$DEPLOY_DIR/recovery.lock" ]]; then
    (( PIN_ONLY && inherited )) || die "recovery in progress; run $DEPLOY_DIR/restore.sh --status. --pin-only requires an inherited operations lock"
    [[ "$RECOVERY_NONCE" =~ ^[0-9a-f]{32}$ && ! -L "$DEPLOY_DIR/recovery.lock" ]] || die "invalid recovery nonce or lock"
    [[ "$(jq -er .nonce "$DEPLOY_DIR/recovery.lock")" == "$RECOVERY_NONCE" ]] || die "recovery nonce does not match"
    [[ "$(jq -er .phase "$DEPLOY_DIR/recovery.lock")" == pin-intent ]] || die "--pin-only requires pin-intent"
  elif (( PIN_ONLY )); then
    die "--pin-only requires recovery.lock and an inherited operations lock"
  elif [[ -n "$RECOVERY_NONCE" ]]; then die "--recovery-nonce requires --pin-only"; fi
  if (( PIN_ONLY )); then
    (( SKIP_HARDEN && ! SKIP_DEPLOY )) || die "--pin-only requires --skip-harden and a deploy target"
  fi
  if [[ "${DEPLOY_DRILL:-}" == 1 ]] && (( ! SKIP_HARDEN )); then
    die "DEPLOY_DRILL requires --skip-harden; it never hardens a development host"
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
    require_deploy_tools_version "$SCRIPT_DIR" "$DEPLOY_DIR" || exit 1
    if (( ! PIN_ONLY )); then install_docker; fi
    # Save before the first pin-only pull, which can retag the current image.
    if (( PIN_ONLY )); then save_release_pin; fi
    preflight_recovery_level
    if (( ! PIN_ONLY )); then save_release_pin; fi
    install_runtime_files
    write_env
    ensure_release_pin
    if (( PIN_ONLY )); then
      if [[ -n "${RESTORE_DRILL_FAULT:-}${RESTORE_DRILL_PAUSE:-}" ]]; then
        [[ "${DEPLOY_DRILL:-}" == 1 && "$DEPLOY_DIR" != /opt/fold && "$DEPLOY_DIR" != /opt/fold/* ]] || die "RESTORE_DRILL hooks require the disposable drill"
        if [[ "${RESTORE_DRILL_PAUSE:-}" == mid-pin ]]; then
          [[ -p "$DEPLOY_DIR/drill.fifo" ]] || die "missing drill FIFO"
          exec 8<>"$DEPLOY_DIR/drill.fifo"
          echo mid-pin >"$DEPLOY_DIR/drill.ready"
          local token=""
          IFS= read -r -t 180 token <&8 && [[ "$token" == continue ]] || die "drill mid-pin barrier failed"
          exec 8>&-
        fi
        # The child tells the parent only after .env is changed. Kill both so
        # the drill sees an actual interruption with the durable pin-intent.
        if [[ "${RESTORE_DRILL_FAULT:-}" == mid-pin ]]; then kill -KILL "$PPID" "$$"; fi
      fi
    fi
    verify_before_up
    if [[ "${DEPLOY_DRILL:-}" != 1 ]] && (( ! PIN_ONLY )); then
      install_smoke_cron
      install_backup_cron
      run_email_dns
    fi
    deploy_stack
    if (( PIN_ONLY )); then echo "provision.sh: pinned only; no services started"; return 0; fi
    run_smoke
    if (( ! DRY_RUN )); then
      if ! write_recovery_floor "$DEPLOY_DIR" "$TARGET_LEVEL"; then
        die "deployed and smoke passed, but could not record ${DEPLOY_DIR}/recovery-policy-floor. The pinned image still enforces the floor. Keep it locally, repair storage/permissions, then rerun ${DEPLOY_DIR}/provision.sh with the same release and options. Do not roll back for this error" 76
      fi
    fi
  fi
  summarize
}

main "$@"
