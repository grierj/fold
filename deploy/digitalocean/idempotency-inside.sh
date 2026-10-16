#!/usr/bin/env bash
# Second half of the container test. Run provision.sh twice for real, with
# systemd and Docker stubbed, and assert the second run changes nothing.
# Called from dry-run-inside.sh after the dry-run assertions.
set -euo pipefail

cd /src

echo "idempotency: installing packages the real run needs"
export DEBIAN_FRONTEND=noninteractive
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod +x /usr/sbin/policy-rc.d
apt-get update
apt-get install -y openssh-server sudo openssl git ca-certificates jq dnsutils
mkdir -p /run/sshd
if [[ ! -s /etc/ssh/ssh_host_ed25519_key ]]; then
  ssh-keygen -A
fi
git config --global --add safe.directory /src
if ! getent group docker >/dev/null; then
  groupadd docker
fi
getent group sudo >/dev/null

cert_dir="$(mktemp -d)"
/usr/bin/openssl req -x509 -newkey rsa:2048 -keyout "$cert_dir/ok.key" -out /tmp/smoke-ok.pem \
  -days 2 -nodes -subj /CN=groups.example.org \
  -addext subjectAltName=DNS:groups.example.org >/dev/null 2>&1
/usr/bin/openssl req -x509 -newkey rsa:2048 -keyout "$cert_dir/wrong.key" -out /tmp/smoke-wrong.pem \
  -days 2 -nodes -subj /CN=wrong.example \
  -addext subjectAltName=DNS:wrong.example >/dev/null 2>&1

expired_dir="$(mktemp -d)"
(
  cd "$expired_dir"
  touch index.txt
  echo 01 >serial
  cat >cnf <<'EOF'
[ca]
default_ca = CA_default
[CA_default]
new_certs_dir = .
database = index.txt
serial = serial
default_md = sha256
policy = policy_any
default_startdate = 20200101000000Z
default_enddate = 20200102000000Z
[policy_any]
commonName = supplied
[req]
distinguished_name = req_dn
prompt = no
[req_dn]
CN = groups.example.org
[v3]
subjectAltName = DNS:groups.example.org
basicConstraints = CA:true
EOF
  /usr/bin/openssl req -new -newkey rsa:2048 -keyout ca.key -out ca.csr -nodes \
    -config cnf -subj /CN=groups.example.org >/dev/null 2>&1
  /usr/bin/openssl ca -selfsign -in ca.csr -keyfile ca.key -out /tmp/smoke-expired.pem \
    -config cnf -batch -extensions v3 >/dev/null 2>&1
)

install -d /usr/local/bin /usr/local/lib /tmp/email-state
cat >/usr/local/bin/openssl <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "s_client" ]]; then
  cat "${SMOKE_TEST_CERT:?}"
  exit 0
fi
exec /usr/bin/openssl "$@"
EOF
cat >/usr/local/bin/docker <<'EOF'
#!/bin/bash
set -euo pipefail
if [[ "${1:-}" == "inspect" ]]; then
  printf '%s\n' healthy
  exit 0
fi
[[ "${1:-}" == "compose" ]] || { echo "docker stub: $*" >&2; exit 1; }
shift
args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --project-directory)
      shift 2
      ;;
    --project-directory=*)
      shift
      ;;
    *)
      args+=("$1")
      shift
      ;;
  esac
done
cmd="${args[0]:-}"
case "$cmd" in
  version|pull|up) exit 0 ;;
  ps)
    if [[ -n "${DOCKER_STUB_PS:-}" ]]; then
      printf '%s\n' "$DOCKER_STUB_PS"
      exit 0
    fi
    printf '%s\n' \
      'db|Up (healthy)|' \
      'app|Up (healthy)|' \
      'caddy|Up (healthy)|0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp'
    ;;
  *)
    echo "docker stub: compose ${cmd}" >&2
    exit 1
    ;;
esac
EOF
cat >/usr/local/bin/ufw <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "status" ]]; then
  printf 'Status: active\n'
fi
exit 0
EOF
cat >/usr/local/bin/systemctl <<'EOF'
#!/bin/bash
exit 0
EOF
cat >/usr/local/bin/apt-get <<'EOF'
#!/bin/bash
exit 0
EOF
cat >/usr/local/bin/apt-cache <<'EOF'
#!/bin/bash
exit 0
EOF
cat >/usr/local/bin/dpkg-reconfigure <<'EOF'
#!/bin/bash
exit 0
EOF
chmod 755 /usr/local/bin/openssl /usr/local/bin/docker \
  /usr/local/bin/ufw /usr/local/bin/systemctl /usr/local/bin/apt-get \
  /usr/local/bin/apt-cache /usr/local/bin/dpkg-reconfigure

cat >/usr/local/bin/curl <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >>/tmp/curl.log
method="GET"
url=""
outfile=""
data=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == "-X" ]]; then method="$arg"; fi
  if [[ "$prev" == "-o" ]]; then outfile="$arg"; fi
  if [[ "$prev" == "--data" ]]; then data="$arg"; fi
  case "$arg" in
    http://*|https://*) url="$arg" ;;
  esac
  prev="$arg"
done
printf '%s %s\n' "$method" "$url" >>/tmp/curl-calls.log
write_body() {
  if [[ -n "$outfile" ]]; then
    printf '%s\n' "$1" >"$outfile"
  else
    printf '%s\n' "$1"
  fi
}
if [[ "$url" == https://groups.example.org/healthz* ]]; then
  exit 0
fi
if [[ "$url" == *api.postmarkapp.com/domains* && "$method" == "POST" ]]; then
  cp /usr/local/lib/fold-postmark-domain.json /tmp/email-state/domain.json
  write_body "$(cat /usr/local/lib/fold-postmark-domain.json)"
  printf '200'
  exit 0
fi
if [[ "$url" == *api.postmarkapp.com/domains* && "$method" == "GET" ]]; then
  if [[ -f /tmp/email-state/domain.json ]]; then
    body="$(jq -n --slurpfile d /tmp/email-state/domain.json '{TotalCount:1,Domains:$d}')"
  else
    body='{"TotalCount":0,"Domains":[]}'
  fi
  write_body "$body"
  printf '200'
  exit 0
fi
if [[ "$url" == *api.cloudflare.com* && ( "$method" == "POST" || "$method" == "PUT" ) ]]; then
  type="$(jq -r '.type' <<<"$data")"
  name="$(jq -r '.name' <<<"$data")"
  content="$(jq -r '.content' <<<"$data")"
  printf '%s' "$content" >"/tmp/email-state/${type}--${name}"
  write_body '{"success":true,"result":{"id":"rec1"}}'
  printf '200'
  exit 0
fi
if [[ "$url" == *api.cloudflare.com* ]]; then
  type="$(printf '%s' "$url" | sed -n 's/.*[?&]type=\([^&]*\).*/\1/p')"
  name="$(printf '%s' "$url" | sed -n 's/.*[?&]name=\([^&]*\).*/\1/p')"
  state="/tmp/email-state/${type}--${name}"
  if [[ -f "$state" ]]; then
    body="$(jq -n --arg type "$type" --arg name "$name" --arg content "$(cat "$state")" \
      '{success:true,result:[{id:"rec1",type:$type,name:$name,content:$content}]}')"
  else
    body='{"success":true,"result":[]}'
  fi
  write_body "$body"
  printf '200'
  exit 0
fi
echo "curl stub: unexpected ${method} ${url}" >&2
exit 1
EOF
chmod 755 /usr/local/bin/curl

cat >/usr/local/lib/fold-postmark-domain.json <<'EOF'
{"ID":1234,"Name":"groups.example.org","SPFHost":"groups.example.org","SPFTextValue":"v=spf1 a mx include:spf.mtasv.net ~all","DKIMPendingHost":"20260101pm._domainkey.groups.example.org","DKIMPendingTextValue":"k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA","DKIMHost":"","DKIMTextValue":"","ReturnPathDomain":"pm-bounces.groups.example.org","ReturnPathDomainCNAMEValue":"pm.mtasv.net"}
EOF

export SMOKE_TEST_CERT=/tmp/smoke-ok.pem
pub=/tmp/fold-dry-run.pub
[[ -s "$pub" ]] || printf '%s\n' 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDryRunFoldProvisionKeyNotARealSecret fold-dry-run' >"$pub"

# shellcheck source=deploy/digitalocean/checks.sh
source deploy/digitalocean/checks.sh
assert_compose_private docker-compose.yml
loopback="$(mktemp)"
cat >"$loopback" <<'EOF'
services:
  db:
    image: postgres:16-alpine
    ports:
      - "127.0.0.1:5432:5432"
EOF
assert_compose_private "$loopback"
bad="$(mktemp)"
cat >"$bad" <<'EOF'
services:
  db:
    image: postgres:16-alpine
    ports:
      - "5432:5432"
EOF
if assert_compose_private "$bad"; then
  echo "idempotency: a public Postgres port was accepted" >&2
  exit 1
fi

run_real() {
  bash deploy/digitalocean/provision.sh \
    --domain groups.example.org \
    --admin-user deploy \
    --ssh-pubkey-file "$pub" \
    --image ghcr.io/grierj/fold:0.0.0-dry-run \
    --source-dir /src \
    --deploy-dir /opt/fold
}

snapshot() {
  local out="$1"
  {
    sha256sum \
      /opt/fold/.env \
      /opt/fold/docker-compose.yml \
      /opt/fold/Caddyfile \
      /opt/fold/smoke.sh \
      /opt/fold/email-dns.sh \
      /opt/fold/upgrade.sh \
      /opt/fold/checks.sh \
      /opt/fold/backup.sh \
      /opt/fold/backup.env.example \
      /etc/ssh/sshd_config.d/00-fold-hardening.conf \
      /etc/sudoers.d/fold-admin \
      /etc/fail2ban/jail.d/fold-sshd.local \
      /etc/apt/apt.conf.d/20auto-upgrades \
      /etc/apt/apt.conf.d/52fold-unattended \
      /etc/cron.d/fold-health \
      /etc/cron.d/fold-backup \
      /etc/logrotate.d/fold-backup \
      /home/deploy/.ssh/authorized_keys \
      /etc/passwd \
      /etc/shadow \
      /etc/group
    stat -c '%a %n' \
      /opt/fold/.env \
      /home/deploy/.ssh/authorized_keys \
      /etc/ssh/sshd_config.d/00-fold-hardening.conf
    sshd -T | grep -E '^(permitrootlogin|passwordauthentication|kbdinteractiveauthentication|pubkeyauthentication|authenticationmethods) '
  } >"$out"
}

echo "idempotency: first provision"
out1="$(run_real 2>&1)"
printf '%s\n' "$out1"
snapshot /tmp/fold-snap-1
secret="$(env_value /opt/fold/.env SESSION_SECRET)"
[[ -n "$secret" ]]
if grep -qF "$secret" <<<"$out1"; then
  echo "idempotency: first run printed SESSION_SECRET" >&2
  exit 1
fi

echo "idempotency: second provision"
out2="$(run_real 2>&1)"
printf '%s\n' "$out2"
snapshot /tmp/fold-snap-2
if ! diff -u /tmp/fold-snap-1 /tmp/fold-snap-2; then
  echo "idempotency: second provision run changed the machine" >&2
  exit 1
fi
grep -q 'user deploy already exists' <<<"$out2"
grep -q '.env already exists' <<<"$out2"
grep -q 'password for deploy already locked' <<<"$out2"
grep -q 'already in group docker' <<<"$out2"
if grep -q 'useradd' <<<"$out2"; then
  echo "idempotency: second run invoked useradd" >&2
  exit 1
fi
if grep -q 'passwd -l' <<<"$out2"; then
  echo "idempotency: second run locked the password again" >&2
  exit 1
fi
if grep -qF "$secret" <<<"$out2"; then
  echo "idempotency: second run printed SESSION_SECRET" >&2
  exit 1
fi
grep -q 'permitrootlogin no' /tmp/fold-snap-2
grep -q 'passwordauthentication no' /tmp/fold-snap-2

expect_fail() {
  local needle="$1"
  shift
  local out status
  set +e
  out="$("$@" 2>&1)"
  status=$?
  set -e
  if [[ "$status" -eq 0 ]]; then
    echo "idempotency: expected failure ($needle)" >&2
    printf '%s\n' "$out" >&2
    exit 1
  fi
  if ! grep -q "$needle" <<<"$out"; then
    echo "idempotency: failure did not mention $needle" >&2
    printf '%s\n' "$out" >&2
    exit 1
  fi
}

chmod 644 /opt/fold/.env
expect_fail 'mode 644' /opt/fold/smoke.sh --deploy-dir /opt/fold
chmod 600 /opt/fold/.env

SMOKE_TEST_CERT=/tmp/smoke-expired.pem expect_fail 'not currently valid' /opt/fold/smoke.sh --deploy-dir /opt/fold
SMOKE_TEST_CERT=/tmp/smoke-wrong.pem expect_fail 'not valid for' /opt/fold/smoke.sh --deploy-dir /opt/fold
export SMOKE_TEST_CERT=/tmp/smoke-ok.pem

DOCKER_STUB_PS=$'db|Up (healthy)|0.0.0.0:5432->5432/tcp\napp|Up (healthy)|\ncaddy|Up (healthy)|0.0.0.0:80->80/tcp' \
  expect_fail '5432' /opt/fold/smoke.sh --deploy-dir /opt/fold
unset DOCKER_STUB_PS

printf '%s\n' 'PermitRootLogin yes' 'PasswordAuthentication yes' >/etc/ssh/sshd_config.d/00-fold-hardening.conf
if assert_ssh_locked; then
  echo "idempotency: root login was accepted" >&2
  exit 1
fi

calls_before="$(grep -c '^POST ' /tmp/curl-calls.log || true)"
skip_out="$(bash deploy/digitalocean/email-dns.sh --domain groups.example.org --deploy-dir /opt/fold 2>&1)"
grep -q 'skipping Postmark' <<<"$skip_out"
grep -q 'dig +short' <<<"$skip_out"
calls_after="$(grep -c '^POST ' /tmp/curl-calls.log || true)"
[[ "$calls_before" == "$calls_after" ]]

export POSTMARK_ACCOUNT_TOKEN=test-token
export CLOUDFLARE_API_TOKEN=cf-test-token
export CLOUDFLARE_ZONE_ID=abc123
: > /tmp/curl-calls.log
bash deploy/digitalocean/email-dns.sh --domain groups.example.org --deploy-dir /opt/fold >/tmp/email-dns-1.out
first_posts="$(grep -c '^POST ' /tmp/curl-calls.log || true)"
if [[ "$first_posts" != "4" ]]; then
  echo "idempotency: expected 4 Postmark/Cloudflare POSTs, got ${first_posts}" >&2
  cat /tmp/curl-calls.log >&2
  exit 1
fi
grep -q 'dig +short TXT groups.example.org' /tmp/email-dns-1.out
: > /tmp/curl-calls.log
bash deploy/digitalocean/email-dns.sh --domain groups.example.org --deploy-dir /opt/fold >/tmp/email-dns-2.out
second_posts="$(grep -c '^POST ' /tmp/curl-calls.log || true)"
if [[ "$second_posts" != "0" ]]; then
  echo "idempotency: second email-dns run issued ${second_posts} POSTs" >&2
  cat /tmp/curl-calls.log >&2
  exit 1
fi
grep -q 'already exists' /tmp/email-dns-2.out
unset POSTMARK_ACCOUNT_TOKEN CLOUDFLARE_API_TOKEN CLOUDFLARE_ZONE_ID

bash deploy/digitalocean/upgrade.sh --dry-run \
  --domain groups.example.org \
  --image ghcr.io/grierj/fold:0.0.1-dry-run \
  --deploy-dir /tmp/fold-up \
  --source-dir /src >/tmp/upgrade-dry.out
[[ ! -e /tmp/fold-up/release.prev ]]
grep -q 'dry-run:' /tmp/upgrade-dry.out
expect_fail 'latest' bash deploy/digitalocean/upgrade.sh --dry-run \
  --domain groups.example.org \
  --image ghcr.io/grierj/fold:latest \
  --deploy-dir /tmp/fold-up \
  --source-dir /src
expect_fail 'release.prev' bash deploy/digitalocean/upgrade.sh --dry-run \
  --rollback --deploy-dir /tmp/fold-up
expect_fail 'not a commit' bash deploy/digitalocean/upgrade.sh --dry-run \
  --commit deadbeef \
  --domain groups.example.org \
  --source-dir /src \
  --deploy-dir /tmp/fold-up

echo "idempotency: ok"
