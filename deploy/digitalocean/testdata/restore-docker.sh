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
  case "$1" in *zero*) printf 'sha256:%064d\n' 3 ;; *bad*) printf 'sha256:%064d\n' 4 ;; *target*|*dev*) printf 'sha256:%064d\n' 2 ;; *backup*) printf 'sha256:%064d\n' 9 ;; sha256:*) echo "$1" ;; *) printf 'sha256:%064d\n' 1 ;; esac
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
# --- Uploads helper containers (M5.2) -----------------------------------------
# A docker run labelled fold.uploads.role is one backup-image helper. The model
# keeps the folders as inode numbers: .up.live is /data/uploads and .up.staged
# is /data/.restore-NONCE/uploads ("none" when absent). A held helper (env
# RESTORE_FIXTURE_HELPER_HOLD=ROLE) stays registered as running until reaped.
helper_label() {
  local want="$1" a
  shift
  for a in "$@"; do
    if [[ "$a" == "$want="* ]]; then echo "${a#*=}"; return; fi
  done
}
up() { read_model ".up.$1"; }
helper_run() {
  local role nonce sub id i j image
  role="$(helper_label fold.uploads.role "$@")"; nonce="$(helper_label fold.recovery.nonce "$@")"
  # The subcommand follows the image id (the first sha256:... argument).
  sub=""; image=""
  for ((i=1;i<=$#;i++)); do
    if [[ "${!i}" == sha256:* ]]; then image="${!i}"; j=$((i+1)); sub="${!j}"; break; fi
  done
  [[ -n "$role" && -n "$sub" ]] || exit 99
  id="helper-$role-$$"
  update --arg id "$id" --arg role "$role" --arg nonce "$nonce" --arg image "$image" '.helpers += [{id:$id,role:$role,nonce:$nonce,image:$image}]'
  if [[ "${RESTORE_FIXTURE_HELPER_HOLD:-}" == "$role" ]]; then
    for ((i=0;i<150;i++)); do
      jq -e --arg id "$id" '.helpers | any(.id==$id)' "$model" >/dev/null || exit 137
      sleep 0.2
    done
  fi
  local rc=0
  helper_effect "$sub" "$@" || rc=$?
  update --arg id "$id" '.helpers |= map(select(.id!=$id))'
  exit "$rc"
}
helper_effect() {
  local sub="$1" want_id="" a prev="" target
  shift
  for a in "$@"; do
    if [[ "$prev" == --identity ]]; then want_id="$a"; fi
    prev="$a"
  done
  case "$sub" in
    uploads-validate)
      echo up-validate >>"$RESTORE_FIXTURE_DIR/events"
      [[ "$(up validateFail)" != true ]] || { echo 'the uploads archive failed validation; nothing was changed' >&2; return 1; }
      (( $(up free) >= $(up need) )) || { echo 'not enough free space' >&2; return 1; }
      printf 'validated members=3 uncompressedBytes=%s freeBytes=%s\n' "$(up need)" "$(up free)" ;;
    uploads-observe)
      jq -r '.up | "observe live=\(.live) staged=\(.staged) complete=\(if .stageState=="complete" and .receipt then "yes" else "no" end) exchange=\(.exchange) drop=\(if .dropDone!="" then "done" elif .dropIntent!="" then "intent" else "none" end) receipt=\(if .receipt then "present" else "absent" end)"' "$model" ;;
    uploads-stage)
      echo up-stage >>"$RESTORE_FIXTURE_DIR/events"
      [[ "$(up exchange)" == none && "$(up dropIntent)" == "" ]] || return 1
      # stageKill=mkdir: the helper dies after creating its empty staged folder
      # and before the receipt names it (pending receipt, no stage inode).
      if [[ "$(up stageKill)" == mkdir ]]; then
        update '.up.receipt=true | .up.stageState="pending" | .up.stageIno="" | .up.staged=(.up.nextIno|tostring) | .up.nextIno+=1 | .up.stageKill=""'
        return 137
      fi
      # A retry over that folder clears it and builds a new one.
      if [[ "$(up stageState)" == pending ]]; then update '.up.staged="none" | .up.stageIno="" | .up.stageState="none"'; fi
      if [[ "$(up stageState)" != complete ]]; then
        update '.up.receipt=true | .up.stageState="extracting" | (if .up.staged=="none" then .up.stageIno=(.up.nextIno|tostring) | .up.staged=.up.stageIno | .up.nextIno+=1 else . end)'
        [[ "$(up stageFail)" != true ]] || { echo 'extraction did not finish; retry the stage' >&2; return 1; }
        update '.up.stageState="complete" | .up.photoStaged=true'
      fi
      printf 'staged inode=%s files=3\n' "$(up stageIno)" ;;
    uploads-exchange)
      if [[ " $* " == *' --probe '* ]]; then
        echo up-probe >>"$RESTORE_FIXTURE_DIR/events"
        [[ "$(up probeFail)" != true ]] || { echo 'this filesystem cannot exchange folders atomically; nothing was changed' >&2; return 1; }
        echo 'probe ok'; return 0
      fi
      echo up-exchange >>"$RESTORE_FIXTURE_DIR/events"
      [[ "$(up exchangeFail)" != true ]] || return 1
      [[ "$(up stageState)" == complete && "$(up dropIntent)" == "" ]] || return 1
      if [[ "$(up exchange)" != "done" ]]; then
        update '.up.tmp=.up.live | .up.live=.up.staged | .up.staged=.up.tmp | (.up.photoLive as $l | .up.photoLive=.up.photoStaged | .up.photoStaged=$l)'
      fi
      update '.up.exchange="done"'
      printf 'exchanged live=%s old=%s\n' "$(up live)" "$(up staged)" ;;
    uploads-drop)
      if [[ " $* " == *' --release '* ]]; then
        echo up-release >>"$RESTORE_FIXTURE_DIR/events"
        [[ "$(up staged)" == none ]] || return 1
        # A receipt that records a tree is released only after its removal.
        if [[ "$(up receipt)" == true && "$(up stageIno)" != "" ]]; then
          if [[ "$(up exchange)" == "done" ]]; then target="$(up origLive)"; else target="$(up stageIno)"; fi
          [[ "$(up dropDone)" == "$target" ]] || { echo 'the receipt records a tree whose removal it never confirmed; nothing was released' >&2; return 1; }
        fi
        update '.up.receipt=false | .up.stageState="none"'
        echo released; return 0
      fi
      echo up-drop >>"$RESTORE_FIXTURE_DIR/events"
      [[ "$(up receipt)" == true ]] || return 1
      [[ "$(up dropFail)" != true ]] || { echo 'could not remove the tree completely; retry the drop' >&2; return 1; }
      # An empty folder under a pending receipt is named by the caller's identity.
      if [[ "$(up stageState)" == pending && "$(up stageIno)" == "" && "$(up staged)" == "$want_id" && "$(up dropIntent)" == "" ]]; then
        update --arg id "$want_id" '.up.stageIno=$id'
      fi
      if [[ "$(up exchange)" == "done" ]]; then target="$(up origLive)"; else target="$(up stageIno)"; fi
      [[ "$want_id" == "$target" ]] || { echo 'that tree is not the one the receipt records; nothing was dropped' >&2; return 1; }
      if [[ "$(up dropDone)" == "$want_id" ]]; then printf 'dropped identity=%s\n' "$want_id"; return 0; fi
      if [[ "$(up dropIntent)" == "" ]]; then
        [[ "$(up staged)" == "$want_id" ]] || { echo 'the tree is missing or is not the recorded one; nothing was dropped' >&2; return 1; }
      fi
      update --arg id "$want_id" '.up.dropIntent=$id | .up.staged="none" | .up.dropDone=$id'
      printf 'dropped identity=%s\n' "$want_id" ;;
    *) echo "unhandled fixture helper: $sub" >&2; exit 99 ;;
  esac
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
    if [[ "$*" == *'fold.uploads.role='* ]]; then helper_run "$@"; fi
    # backup.sh's uploads archive (alpine tar czf /backup/NAME): the file appears in the backup folder.
    if [[ "$*" == *' alpine tar czf '* ]]; then
      host=""
      for a in "$@"; do case "$a" in *:/backup) host="${a%:/backup}" ;; esac; done
      for a in "$@"; do case "$a" in /backup/*) printf 'UPLOADS' >"$host/${a#/backup/}" ;; esac; done
      exit 0
    fi
    image="${args[${#args[@]}-3]}"
    case "$image" in
      *bad*|"$(printf 'sha256:%064d' 4)") echo '{"recoveryPolicy":"bad"}' ;;
      *zero*|"$(printf 'sha256:%064d' 3)") echo '{"version":"legacy"}' ;;
      *) jq -cn --argjson level "$(read_model '.restoreUploadsLevel // 1')" '{recoveryPolicy:1,restoreUploads:$level}' ;;
    esac
    exit 0 ;;
  volume)
    # Only the uploads volume exists, and only when the model says so. A listing
    # succeeds and finds nothing: the backups volume is absent here, which
    # backup.sh's expiry reads as "no copies owed". A failing listing would be a
    # failed observation, and the shipped code rightly keeps that work pending.
    if [[ "$2" == ls ]]; then exit 0; fi
    if [[ "$2" == inspect && "$(read_model '.volume.exists // false')" == true && "${*: -1}" == "$(read_model '.volume.name')" ]]; then read_model '.volume.createdAt'; exit 0; fi
    exit 1 ;;
  ps)
    if [[ "$*" == *'fold.uploads.role'* ]]; then
      if [[ "$*" == *'--filter id='* ]]; then
        want="${*#*--filter id=}"; want="${want%% *}"
        jq -r --arg id "$want" '.helpers[]? | select(.id==$id) | .id' "$model"
      else jq -r '.helpers[]?.id' "$model"; fi
      exit 0
    fi
    if [[ "$*" == *'--filter id='* ]]; then exit 0; fi
    if [[ "$(read_model '.recovery.exists')" == true ]]; then echo recovery; fi; exit 0 ;;
  rm)
    for id in "$@"; do
      case "$id" in
        helper-*) update --arg id "$id" '.helpers |= map(select(.id!=$id))'; echo "rm-helper" >>"$RESTORE_FIXTURE_DIR/events"; exit 0 ;;
      esac
    done
    update '.recovery.exists=false | .recovery.running=false'; echo rm-recovery >>"$RESTORE_FIXTURE_DIR/events"; exit 0 ;;
  exec) [[ "$(read_model '.healthFail // false')" != true ]]; exit ;;
  inspect)
    fmt="$3"; id="${*: -1}"
    if [[ "$id" == helper-* ]]; then
      case "$fmt" in
        '{{index .Config.Labels "fold.recovery.nonce"}}') jq -r --arg id "$id" '.helpers[] | select(.id==$id) | .nonce' "$model"; exit 0 ;;
        '{{.State.Running}}') echo true; exit 0 ;;
      esac
      exit 99
    fi
    if [[ "$id" == recovery ]]; then svc=recovery; else svc="$id"; fi
    case "$fmt" in
      *'index .Config.Labels "fold.recovery.nonce"'*restart=*) : ;;
      '{{index .Config.Labels "fold.recovery.nonce"}}') read_model .recovery.nonce; exit 0 ;;
      '{{or .State.Running .State.Restarting}}'|'{{.State.Running}}') read_model ".${svc}.running"; exit 0 ;;
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
    [[ "$(read_model '.configFail // false')" != true ]] || exit 1
    ref="$(sed -n 's/.*image: \(.*\) # fold-app-image$/\1/p' "$dir/docker-compose.yml")"
    [[ -n "$ref" ]] || ref="fixture:$(sed -n 's/^FOLD_VERSION=//p' "$dir/.env")"
    # The backup service appears only when the model defines it; .backup.coord
    # says how its coordination is wired (ok, unset, rw, other, other-dir). It
    # resolves with the shipped identity (user 65532:65532, cap_drop ALL) unless
    # the model overrides it: .backup.user and .backup.capDrop ("unset" omits the
    # key) and .backup.privileged. Its archive, status and uploads mounts are the
    # shipped ones.
    jq -n --arg ref "$ref" --arg dir "$dir" --argjson backup "$(read_model '.backup // null')" --arg vol "$(read_model '.volume.name // "restore-fixture_uploads"')" '
      {name:"restore-fixture",
       services:({app:{image:$ref,volumes:[{type:"volume",source:"uploads",target:"/data"}]},caddy:{ports:[{published:"45000"}]}}
         + (if ($backup!=null and $backup.defined==true) then {backup:({image:($backup.image // "fixture:backup"),
              environment:(if $backup.coord=="unset" then {} else {FOLD_RECOVERY_DIR:(if $backup.coord=="other" then "/elsewhere" else "/coord" end)} end),
              volumes:[{type:"bind",source:(if $backup.coord=="other-dir" then "/elsewhere" else $dir end),target:"/coord",read_only:($backup.coord!="rw")},
                {type:"volume",source:"backups",target:"/backups"},{type:"volume",source:"backup-status",target:"/backup-status"},
                {type:"volume",source:"uploads",target:"/data",read_only:true}]}
              + (if $backup.user=="unset" then {} else {user:($backup.user // "65532:65532")} end)
              + (if $backup.capDrop=="unset" then {} else {cap_drop:($backup.capDrop // ["ALL"])} end)
              + (if $backup.privileged==true then {privileged:true} else {} end))} else {} end)),
       volumes:{uploads:{name:$vol},backups:{name:"restore-fixture_backups"},"backup-status":{name:"restore-fixture_backup-status"}}}'
    exit 0 ;;
  ps)
    if [[ "$*" == *'-q'* ]]; then
      for svc in app caddy; do [[ " $* " != *" $svc "* ]] || echo "$svc"; done
      if [[ " $* " == *" backup "* ]]; then
        [[ "$(read_model '.backup.defined // false')" == true ]] || exit 1
        echo backup
      fi
    else echo 'fixture app/caddy/recovery state'; fi
    exit 0 ;;
  run)
    # fold restore settle-uploads, run in the recovery service. The container
    # carries the recovery service label while it runs; --rm removes it.
    echo "settle-start image=${FOLD_RECOVERY_IMAGE:-} nonce=${FOLD_RECOVERY_NONCE:-}" >>"$RESTORE_FIXTURE_DIR/events"
    update --arg nonce "${FOLD_RECOVERY_NONCE:-}" --arg image "${FOLD_RECOVERY_IMAGE:-}" '.recovery.exists=true | .recovery.running=true | .recovery.nonce=$nonce | .recovery.image=$image'
    if [[ "${RESTORE_FIXTURE_SETTLE_HOLD:-0}" == 1 ]]; then
      for ((i=0;i<150;i++)); do
        [[ "$(read_model '.recovery.exists')" == true ]] || exit 137
        sleep 0.2
      done
    fi
    rc="$(read_model '.settle.rc // 0')"
    if [[ "$rc" == 0 ]]; then update '.up.photoLive=false'; fi
    update '.recovery.exists=false | .recovery.running=false'
    echo "settle-end rc=$rc" >>"$RESTORE_FIXTURE_DIR/events"
    # Counts only, as the command prints them.
    if [[ -n "$(read_model '.settle.out // empty')" ]]; then read_model '.settle.out'
    elif [[ "$rc" == 0 ]]; then
      echo '{"ownersScanned":2,"erasedCleared":1,"unownedCleared":0,"remaining":0,"unexpected":0}'
    else echo '{"ownersScanned":2,"erasedCleared":0,"unownedCleared":0,"remaining":1,"unexpected":0}'; fi
    exit "$rc" ;;
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
    if [[ " $* " == *" backup "* && "$(read_model '.backupPullFail // false')" == true ]]; then exit 1; fi
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
elif [[ "$sql" == *'CREATE TEMP TABLE erased_ids'* ]]; then
  # The archive-side comparison prints the revived ids, one per line.
  jq -r '(.count // 2) as $n | if $n>0 then range(1; $n+1) else empty end' "$model"
elif [[ "$sql" == *'COPY (SELECT l.id'* ]]; then
  [[ "${RESTORE_FIXTURE_TIMES_FAIL:-0}" == 0 ]] || exit 1
  jq -r '(.count // 2) as $n | if $n>0 then range(1; $n+1) | "\(.)\t2026-11-14T10:00:00Z" else empty end' "$model"
elif [[ "$sql" == *'CREATE TEMP TABLE listed'* ]]; then read_model '.remaining // 0'
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
