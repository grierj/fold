#!/usr/bin/env bash
# Create a Postmark sender domain and, when a Cloudflare token is set, the
# SPF TXT, DKIM TXT, and return-path CNAME. Skips when the tokens are unset.
# Never creates a Postmark account, a Cloudflare zone, or any other paid resource.
#
#   POSTMARK_ACCOUNT_TOKEN   account token (not the server token)
#   CLOUDFLARE_API_TOKEN     DNS edit on one zone
#   CLOUDFLARE_ZONE_ID
#   FOLD_MAIL_DOMAIN         overrides --domain
#
# A second run does not create a second domain or a second DNS record.
# sudo drops the environment unless you pass --preserve-env.
set -euo pipefail

DRY_RUN=0
DOMAIN=""
DEPLOY_DIR="/opt/fold"

die() {
  printf 'email-dns: %s\n' "$1" >&2
  exit "${2:-1}"
}

usage() {
  cat <<'EOF'
Usage: email-dns.sh [--domain groups.example.org] [--deploy-dir /opt/fold] [--dry-run]

Does nothing unless POSTMARK_ACCOUNT_TOKEN is set. With CLOUDFLARE_API_TOKEN
and CLOUDFLARE_ZONE_ID, upserts SPF (TXT), DKIM (TXT), and the return-path
CNAME. Prints dig commands either way. --dry-run makes no API calls.
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
    --domain)
      need_value "$@"
      DOMAIN="$2"
      shift 2
      ;;
    --domain=*)
      DOMAIN="${1#*=}"
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
    *)
      die "unknown option: $1"
      ;;
  esac
done

if [[ -z "$DOMAIN" && -n "${FOLD_MAIL_DOMAIN:-}" ]]; then
  DOMAIN="$FOLD_MAIL_DOMAIN"
fi
if [[ -z "$DOMAIN" && -f "$DEPLOY_DIR/.env" ]]; then
  line=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      DOMAIN=*) DOMAIN="${line#DOMAIN=}" ;;
    esac
  done <"$DEPLOY_DIR/.env"
  if [[ "$DOMAIN" == \"*\" && "$DOMAIN" == *\" ]]; then
    DOMAIN="${DOMAIN:1:${#DOMAIN}-2}"
  fi
fi
if [[ -n "${FOLD_MAIL_DOMAIN:-}" ]]; then
  DOMAIN="$FOLD_MAIL_DOMAIN"
fi
DOMAIN="$(printf '%s' "$DOMAIN" | tr '[:upper:]' '[:lower:]')"

print_dig_help() {
  local name="${1:-example.org}"
  cat <<EOF
email-dns: check the records with:
  dig +short TXT ${name}
  dig +short TXT <selector>._domainkey.${name}
  dig +short CNAME pm-bounces.${name}
EOF
}

if (( DRY_RUN )); then
  echo "dry-run: no Postmark or Cloudflare calls"
  echo "dry-run: skipped unless POSTMARK_ACCOUNT_TOKEN is set"
  echo "dry-run: with CLOUDFLARE_API_TOKEN and CLOUDFLARE_ZONE_ID, upsert SPF TXT, DKIM TXT, and the return-path CNAME"
  print_dig_help "${DOMAIN:-example.org}"
  exit 0
fi

if [[ -z "${POSTMARK_ACCOUNT_TOKEN:-}" ]]; then
  echo "email-dns: skipping Postmark (POSTMARK_ACCOUNT_TOKEN is unset)"
  print_dig_help "${DOMAIN:-example.org}"
  exit 0
fi

[[ "$DOMAIN" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$ ]] \
  || die "--domain must be a hostname such as groups.example.org"
[[ "${POSTMARK_ACCOUNT_TOKEN}" =~ ^[A-Za-z0-9._~+/-]+$ ]] \
  || die "POSTMARK_ACCOUNT_TOKEN has characters this script will not put in a curl config"

token_ok() {
  [[ "$1" =~ ^[A-Za-z0-9._~+/-]+$ ]]
}

command -v jq >/dev/null 2>&1 || die "jq is required to read the Postmark response (apt-get install jq)"
command -v curl >/dev/null 2>&1 || die "curl is required"

api() {
  local method="$1" url="$2" header_line="$3" data="$4" outfile="$5"
  local cfg code
  cfg="$(mktemp)"
  chmod 600 "$cfg"
  printf 'header = "%s"\n' "$header_line" >"$cfg"
  if [[ -n "$data" ]]; then
    code="$(curl --config "$cfg" --proto '=https' --max-time 30 -sS -o "$outfile" -w '%{http_code}' \
      -X "$method" -H 'Accept: application/json' -H 'Content-Type: application/json' \
      --data "$data" "$url")" || {
      rm -f "$cfg"
      die "curl ${method} failed for ${url%%\?*}"
    }
  else
    code="$(curl --config "$cfg" --proto '=https' --max-time 30 -sS -o "$outfile" -w '%{http_code}' \
      -X "$method" -H 'Accept: application/json' "$url")" || {
      rm -f "$cfg"
      die "curl ${method} failed for ${url%%\?*}"
    }
  fi
  rm -f "$cfg"
  printf '%s' "$code"
}

show_http_error() {
  local what="$1" code="$2" file="$3" body
  body="$(head -c 400 "$file" 2>/dev/null || true)"
  die "${what} returned ${code}: ${body}"
}

postmark_header="X-Postmark-Account-Token: ${POSTMARK_ACCOUNT_TOKEN}"
body="$(mktemp)"
chmod 600 "$body"
trap 'rm -f "$body"' EXIT

code="$(api GET "https://api.postmarkapp.com/domains?count=300&offset=0" "$postmark_header" "" "$body")"
[[ "$code" == "200" ]] || show_http_error "Postmark list domains" "$code" "$body"
total="$(jq -r '.TotalCount // 0' "$body")"
if [[ "$total" =~ ^[0-9]+$ ]] && (( total > 300 )); then
  die "Postmark account has ${total} domains; this script only looks at the first 300"
fi
domain_json="$(jq -c --arg name "$DOMAIN" '.Domains[]? | select(.Name == $name)' "$body" | head -n 1)"
if [[ -z "$domain_json" ]]; then
  payload="$(jq -n --arg name "$DOMAIN" '{Name:$name}')"
  code="$(api POST "https://api.postmarkapp.com/domains" "$postmark_header" "$payload" "$body")"
  [[ "$code" == "200" || "$code" == "201" ]] || show_http_error "Postmark create domain" "$code" "$body"
  domain_json="$(jq -c '.' "$body")"
  echo "email-dns: created Postmark domain ${DOMAIN}"
else
  echo "email-dns: Postmark domain ${DOMAIN} already exists"
fi

dkim_host="$(jq -r 'if (.DKIMPendingHost // "") != "" then .DKIMPendingHost else (.DKIMHost // "") end' <<<"$domain_json")"
dkim_value="$(jq -r 'if (.DKIMPendingTextValue // "") != "" then .DKIMPendingTextValue else (.DKIMTextValue // "") end' <<<"$domain_json")"
spf_host="$(jq -r '.SPFHost // empty' <<<"$domain_json")"
spf_value="$(jq -r '.SPFTextValue // empty' <<<"$domain_json")"
rp_host="$(jq -r '.ReturnPathDomain // empty' <<<"$domain_json")"
rp_value="$(jq -r '.ReturnPathDomainCNAMEValue // empty' <<<"$domain_json")"
[[ -n "$dkim_host" && -n "$dkim_value" ]] || die "Postmark did not return a DKIM host and value"
[[ -n "$spf_host" && -n "$spf_value" ]] || die "Postmark did not return an SPF host and value"
[[ -n "$rp_host" && -n "$rp_value" ]] || die "Postmark did not return a return-path host and CNAME"

cf_token="${CLOUDFLARE_API_TOKEN:-}"
cf_zone="${CLOUDFLARE_ZONE_ID:-}"
if [[ -z "$cf_token" || -z "$cf_zone" ]]; then
  echo "email-dns: skipping Cloudflare (set CLOUDFLARE_API_TOKEN and CLOUDFLARE_ZONE_ID to write records)"
  echo "email-dns: SPF TXT ${spf_host} = ${spf_value}"
  echo "email-dns: DKIM TXT ${dkim_host} = ${dkim_value}"
  echo "email-dns: CNAME ${rp_host} = ${rp_value}"
else
  token_ok "$cf_token" || die "CLOUDFLARE_API_TOKEN has characters this script will not put in a curl config"
  [[ "$cf_zone" =~ ^[A-Za-z0-9]+$ ]] || die "CLOUDFLARE_ZONE_ID is not a zone id"
  cf_header="Authorization: Bearer ${cf_token}"

  # kind name content — spf is special-cased by the caller.
  upsert_record() {
    local type="$1" name="$2" content="$3" existing id current payload
    code="$(api GET "https://api.cloudflare.com/client/v4/zones/${cf_zone}/dns_records?type=${type}&name=${name}" \
      "$cf_header" "" "$body")"
    [[ "$code" == "200" ]] || show_http_error "Cloudflare list ${type} ${name}" "$code" "$body"
    jq -e '.success == true' "$body" >/dev/null || show_http_error "Cloudflare list ${type} ${name}" "success=false" "$body"
    existing="$(jq -r --arg type "$type" --arg name "$name" --arg content "$content" '
      [.result[]? | select(.type == $type and .name == $name)] | .[0] // empty
      | if . == null or . == "" then "" else "\(.id)\t\(.content)" end
    ' "$body")"
    if [[ "$type" == "TXT" && "$content" == v=spf1* ]]; then
      existing="$(jq -r --arg name "$name" '
        [.result[]? | select(.type == "TXT" and .name == $name and (.content | startswith("v=spf1")))] | .[0] // empty
        | if . == null or . == "" then "" else "\(.id)\t\(.content)" end
      ' "$body")"
      if [[ -n "$existing" ]]; then
        current="${existing#*$'\t'}"
        if [[ "$current" == *"include:spf.mtasv.net"* ]]; then
          echo "email-dns: SPF TXT ${name} already includes spf.mtasv.net"
          return 0
        fi
        die "SPF TXT for ${name} does not include spf.mtasv.net. Add that include yourself; this script will not replace the record. Current: ${current}"
      fi
    fi
    if [[ -n "$existing" ]]; then
      id="${existing%%$'\t'*}"
      current="${existing#*$'\t'}"
      if [[ "$current" == "$content" ]]; then
        echo "email-dns: ${type} ${name} already set"
        return 0
      fi
      payload="$(jq -n --arg type "$type" --arg name "$name" --arg content "$content" \
        '{type:$type,name:$name,content:$content,ttl:3600,proxied:false}')"
      code="$(api PUT "https://api.cloudflare.com/client/v4/zones/${cf_zone}/dns_records/${id}" \
        "$cf_header" "$payload" "$body")"
      [[ "$code" == "200" ]] || show_http_error "Cloudflare update ${type} ${name}" "$code" "$body"
      echo "email-dns: updated ${type} ${name}"
      return 0
    fi
    payload="$(jq -n --arg type "$type" --arg name "$name" --arg content "$content" \
      '{type:$type,name:$name,content:$content,ttl:3600,proxied:false}')"
    code="$(api POST "https://api.cloudflare.com/client/v4/zones/${cf_zone}/dns_records" \
      "$cf_header" "$payload" "$body")"
    [[ "$code" == "200" ]] || show_http_error "Cloudflare create ${type} ${name}" "$code" "$body"
    echo "email-dns: created ${type} ${name}"
  }

  upsert_record TXT "$spf_host" "$spf_value"
  upsert_record TXT "$dkim_host" "$dkim_value"
  upsert_record CNAME "$rp_host" "$rp_value"
fi

run_dig() {
  local type="$1" name="$2" needle="$3" saw
  echo "dig +short ${type} ${name}"
  if ! command -v dig >/dev/null 2>&1; then
    echo "email-dns: dig is not installed; run the command above from a machine that has it" >&2
    return 0
  fi
  saw="$(dig +time=2 +tries=1 +short "$type" "$name" || true)"
  printf '%s\n' "$saw"
  if [[ "$saw" != *"$needle"* ]]; then
    echo "email-dns: ${name} does not show ${needle} yet. DNS can take time; run the dig command again." >&2
  fi
}

run_dig TXT "$spf_host" "spf.mtasv.net"
# The key material is public. Match a short unique prefix so a split TXT still counts.
run_dig TXT "$dkim_host" "${dkim_value:0:24}"
run_dig CNAME "$rp_host" "${rp_value%.}"
echo "email-dns: set FOLD_SMTP_URL (smtp.postmarkapp.com:587) and FOLD_EMAIL_FROM in ${DEPLOY_DIR}/.env, then reload Compose"
