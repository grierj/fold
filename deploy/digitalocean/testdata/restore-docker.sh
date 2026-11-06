#!/usr/bin/env bash
# Stateful Docker/psql double, used only through restore-cli-fixtures.sh's PATH.
# shellcheck disable=SC2016 # jq receives its variables through --arg.
set -euo pipefail
: "${RESTORE_FIXTURE_DIR:?fixture runner only}"
model="$RESTORE_FIXTURE_DIR/model.json"
printf '%s\n' "$*" >>"$RESTORE_FIXTURE_DIR/calls"
read_model() { jq -r "$1" "$model"; }
update() { local tmp; tmp="$(mktemp "$RESTORE_FIXTURE_DIR/.model.XXXXXX")"; jq "$@" "$model" >"$tmp"; mv "$tmp" "$model"; }
image_id() {
  local mapped
  mapped="$(jq -r --arg ref "$1" '.images[$ref] // empty' "$model")"
  if [[ -n "$mapped" ]]; then echo "$mapped"; return; fi
  case "$1" in *zero*) printf 'sha256:%064d\n' 3 ;; *bad*) printf 'sha256:%064d\n' 4 ;; *target*|*dev*) printf 'sha256:%064d\n' 2 ;; sha256:*) echo "$1" ;; *) printf 'sha256:%064d\n' 1 ;; esac
}
# Each registry response can differ, including moves between either pin pull.
pull_image() {
  local ref="$1" count next
  update --arg ref "$ref" '.pullCounts[$ref]=((.pullCounts[$ref] // 0)+1)'
  count="$(jq -r --arg ref "$ref" '.pullCounts[$ref]' "$model")"
  if [[ "$(jq -r --arg ref "$ref" '.pullFailAt[$ref] // 0' "$model")" == "$count" ]]; then return 1; fi
  next="$(jq -r --arg ref "$ref" --argjson count "$count" '.moveSequence[$ref][$count-1] // .moveOnPull[$ref] // empty' "$model")"
  [[ -z "$next" ]] || update --arg ref "$ref" --arg id "$next" '.images[$ref]=$id'
}
args=("$@")
case "$1" in
  pull)
    pull_image "$2"
    if [[ -f "$RESTORE_FIXTURE_DIR/site/recovery.lock" ]]; then
      echo "pin-check-pull:$2" >>"$RESTORE_FIXTURE_DIR/events"
    else echo "pull:$2" >>"$RESTORE_FIXTURE_DIR/events"; fi
    exit 0 ;;
  image)
    if [[ "$2" == tag ]]; then update --arg ref "$4" --arg id "$3" '.images[$ref]=$id'; exit 0; fi
    [[ "${RESTORE_FIXTURE_INSPECT_FAIL:-0}" == 0 ]] || exit 1; image_id "${*: -1}"; exit 0 ;;
  run)
    image="${args[${#args[@]}-3]}"
    case "$image" in *bad*|"$(printf 'sha256:%064d' 4)") echo '{"recoveryPolicy":"bad"}' ;; *zero*|"$(printf 'sha256:%064d' 3)") echo '{"version":"legacy"}' ;; *) echo '{"recoveryPolicy":1}' ;; esac
    exit 0 ;;
  volume) exit 1 ;; # No uploads volume in the command-only fixture.
  ps) if [[ "$(read_model '.recovery.exists')" == true ]]; then echo recovery; fi; exit 0 ;;
  rm) update '.recovery.exists=false | .recovery.running=false'; echo rm-recovery >>"$RESTORE_FIXTURE_DIR/events"; exit 0 ;;
  exec) [[ "$(read_model '.healthFail // false')" != true ]]; exit ;;
  inspect)
    fmt="$3"; id="${*: -1}"
    if [[ "$id" == recovery ]]; then svc=recovery; else svc="$id"; fi
    case "$fmt" in
      *'index .Config.Labels "fold.recovery.nonce"'*restart=*) : ;;
      '{{index .Config.Labels "fold.recovery.nonce"}}') read_model .recovery.nonce; exit 0 ;;
      '{{or .State.Running .State.Restarting}}') read_model ".${svc}.running"; exit 0 ;;
      '{{.Image}}') read_model ".${svc}.image"; exit 0 ;;
      '{{.HostConfig.RestartPolicy.Name}}') if [[ "$svc" == recovery ]]; then echo no; else echo unless-stopped; fi; exit 0 ;;
      '{{.HostConfig.RestartPolicy.Name}}|{{.State.Running}}|'*) printf 'no|%s|%s\n' "$(read_model .recovery.running)" "$(read_model '.recovery.health // "healthy"')"; exit 0 ;;
      '{{json .NetworkSettings.Ports}}') echo '{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"45000"}]}'; exit 0 ;;
    esac
    printf '%s running=%s restart=no image=%s\n' "$svc" "$(read_model ".${svc}.running")" "$(read_model ".${svc}.image")"
    exit 0 ;;
  compose) shift ;;
  *) echo "unhandled fixture docker: $*" >&2; exit 99 ;;
esac
[[ "$1" == --project-directory ]] || exit 99
dir="$2"; shift 2
# Explicit -f and profile flags do not change the model's project.
while [[ "${1:-}" == -f || "${1:-}" == --profile ]]; do shift 2; done
case "$1" in
  config)
    ref="$(sed -n 's/.*image: \(.*\) # fold-app-image$/\1/p' "$dir/docker-compose.yml")"
    [[ -n "$ref" ]] || ref="fixture:$(sed -n 's/^FOLD_VERSION=//p' "$dir/.env")"
    jq -n --arg ref "$ref" '{name:"restore-fixture",services:{app:{image:$ref},caddy:{ports:[{published:"45000"}]}}}'
    exit 0 ;;
  ps)
    if [[ "$*" == *'-q'* ]]; then
      for svc in app caddy; do [[ " $* " != *" $svc "* ]] || echo "$svc"; done
    else echo 'fixture app/caddy/recovery state'; fi
    exit 0 ;;
  stop)
    svc="${*: -1}"
    [[ "${RESTORE_DRILL_FAULT:-}" != stop-fails ]] || exit 1
    update --arg svc "$svc" '.[$svc].running=false'
    echo "stop-$svc" >>"$RESTORE_FIXTURE_DIR/events"
    exit 0 ;;
  up)
    svc="${*: -1}"
    [[ "$(read_model '.upFail // ""')" != "$svc" ]] || exit 1
    id="${FOLD_RECOVERY_IMAGE:-$(printf 'sha256:%064d' 1)}"
    update --arg svc "$svc" --arg image "$id" --arg nonce "${FOLD_RECOVERY_NONCE:-}" \
      '.[$svc].running=true | .[$svc].image=$image | if $svc=="recovery" then .recovery.exists=true | .recovery.nonce=$nonce | .recovery.health="healthy" else . end'
    echo "start-$svc" >>"$RESTORE_FIXTURE_DIR/events"
    exit 0 ;;
  pull)
    ref="$(sed -n 's/.*image: \(.*\) # fold-app-image$/\1/p' "$dir/docker-compose.yml")"
    pull_image "$ref"
    echo "pin-pull:$ref" >>"$RESTORE_FIXTURE_DIR/events"
    exit 0 ;;
  build|version) exit 0 ;;
  exec) shift ;;
  *) echo "unhandled fixture compose: $*" >&2; exit 99 ;;
esac
[[ "$1" == -T && "$2" == db ]] || exit 99
shift 2
case "$1" in
  sh) printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\nfixture 100000000 0 %s 0%% /data\n' "${RESTORE_FIXTURE_SPACE:-99999999}"; exit 0 ;;
  pg_dump)
    [[ -e /proc/self/fd/6 ]] || exit 99
    [[ "$(stat -Lc '%d:%i' /proc/self/fd/6)" == "$(stat -c '%d:%i' "$dir/backup.lock")" ]] || exit 99
    # A fresh description cannot acquire it while this backup owns fd 6.
    if flock -n "$dir/backup.lock" true; then exit 99; fi
    printf 'fixture backup archive\n'; exit 0 ;;
  pg_restore)
    input="$(cat)"
    if [[ " $* " == *' -l '* ]]; then [[ "$input" != bad-list* ]]; exit; fi
    [[ "$input" != bad-data* && "${RESTORE_FIXTURE_RESTORE_FAIL:-0}" == 0 ]] || exit 1
    echo archive-restore >>"$RESTORE_FIXTURE_DIR/events"
    exit 0 ;;
  psql) shift ;;
  *) exit 99 ;;
esac
sql=""; db=""; name=""
while (( $# )); do
  case "$1" in
    -d) db="$2"; shift 2 ;;
    -c) sql+="$2"$'\n'; shift 2 ;;
    -v) [[ "$2" != name=* ]] || name="${2#name=}"; shift 2 ;;
    -U|-F) shift 2 ;;
    *) shift ;;
  esac
done
[[ -n "$sql" ]] || sql="$(cat)"
printf '%s\n' "$sql" >>"$RESTORE_FIXTURE_DIR/sql"
if [[ "$sql" == *'SELECT datname,oid,pg_database_size'* ]]; then
  pattern="$(sed -n "s/.*datname ~ '\([^']*\)'.*/\1/p" <<<"$sql")"
  jq -r --arg pattern "$pattern" '.dbs | to_entries | sort_by(.key)[] | select(.key=="fold" or (.key|test($pattern))) | [.key,.value.oid,"100",(if .value.allow then "t" else "f" end)] | join("|")' "$model"
elif [[ "$sql" == *'SELECT datname FROM pg_database WHERE datname ~'* ]]; then
  pattern="$(sed -n "s/.*datname ~ '\([^']*\)'.*/\1/p" <<<"$sql")"
  jq -r --arg pattern "$pattern" '.dbs|keys[]|select(test($pattern))' "$model"
elif [[ "$sql" == *'SELECT oid FROM pg_database'* ]]; then
  name="$(sed -n "s/.*datname='\([^']*\)'.*/\1/p" <<<"$sql")"; jq -r --arg n "$name" '.dbs[$n].oid // empty' "$model"
elif [[ "$sql" == *'json_build_object('* ]]; then echo '{"encoding":"UTF8","provider":"c","collate":"C","ctype":"C"}'
elif [[ "$sql" == *'COALESCE((SELECT pg_database_size'* ]]; then echo 100
elif [[ "$sql" == *pg_stat_activity* ]]; then
  echo W-check >>"$RESTORE_FIXTURE_DIR/events"
  extra=0
  [[ "$sql" != *fold_restore_* ]] || extra="$(read_model '.outsideRClients // 0')"
  jq -r --argjson extra "$extra" '(.outsideClients // 0) + $extra + (if .recovery.exists and .recovery.running then 1 else 0 end)' "$model"
elif [[ "$sql" == *'COPY (SELECT id FROM users'* ]]; then [[ "${RESTORE_FIXTURE_SOURCE_UNREADABLE:-0}" == 0 ]] || exit 1; printf '1\n2\n'
elif [[ "$sql" == *'CREATE TEMP TABLE erased_ids'* ]]; then read_model '.count // 2'
elif [[ "$sql" == *'SELECT datallowconn'* ]]; then echo t
elif [[ "$sql" == CREATE\ DATABASE* ]]; then
  [[ -n "$name" ]] || exit 99
  update --arg n "$name" '.nextOid+=1 | .dbs[$n]={oid:(.nextOid|tostring),allow:true}'
elif [[ "$sql" == *'RENAME TO'* ]]; then
  # Both renames in one fixture command are atomic, like PG16. Check every
  # source/target first in a temporary model, then publish the result once.
  tmp="$(mktemp "$RESTORE_FIXTURE_DIR/.rename.XXXXXX")"; cp "$model" "$tmp"
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    if [[ "$line" =~ ALTER\ DATABASE\ \"?([a-z0-9_]+)\"?\ RENAME\ TO\ \"?([a-z0-9_]+)\"? ]]; then
      from="${BASH_REMATCH[1]}"; to="${BASH_REMATCH[2]}"
      jq -e --arg a "$from" --arg b "$to" '.dbs[$a]!=null and .dbs[$b]==null' "$tmp" >/dev/null || { rm "$tmp"; exit 1; }
      jq --arg a "$from" --arg b "$to" '.dbs[$b]=.dbs[$a] | del(.dbs[$a])' "$tmp" >"$tmp.next"; mv "$tmp.next" "$tmp"
    else rm "$tmp"; exit 99; fi
  done <<<"$sql"
  mv "$tmp" "$model"
elif [[ "$sql" == *'ALLOW_CONNECTIONS'* ]]; then
  allow=false; [[ "$sql" != *'true'* ]] || allow=true
  update --argjson allow "$allow" '.dbs.fold.allow=$allow'
elif [[ "$sql" == DROP\ DATABASE* ]]; then
  [[ "${RESTORE_FIXTURE_DROP_FAIL:-0}" == 0 ]] || exit 1
  name="$(sed -n 's/DROP DATABASE "\([a-z0-9_]*\)".*/\1/p' <<<"$sql")"
  [[ -n "$name" && "$name" != fold ]] || exit 99
  update --arg n "$name" 'del(.dbs[$n])'
else echo "unhandled fixture SQL on $db: $sql" >&2; exit 99; fi
