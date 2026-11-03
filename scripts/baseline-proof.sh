#!/usr/bin/env bash
# Generate and prove the single baseline migration (R9, DEC-138, M7.7 S2).
#
# The baseline is never written by hand. `generate` migrates a scratch
# database with the frozen pre-squash chain, dumps it with pg_dump, applies the
# fixed cleanup list of docs/plan/briefs/audit-2026-11/R9-baseline-squash.md
# section 5.2 and proves the result by a second, independent dump. `proof`
# repeats the proof for the committed tree without regenerating anything, so
# later slices (S3, S4) can reuse it. After S2 merges, `generate` refuses to run:
# the tree no longer holds the frozen chain, and regenerating would overwrite
# the S3 and S4 edits.
#
# Usage:
#   scripts/baseline-proof.sh generate --server-url URL --evidence-dir DIR [options]
#   scripts/baseline-proof.sh proof    --server-url URL --evidence-dir DIR [options]
#
# Options:
#   --server-url URL     The scratch PostgreSQL server as the Go binaries reach
#                        it: postgres://USER:PASS@HOST:PORT[?query], a superuser,
#                        no database path. Required. The server must hold no
#                        database of value: every database other than postgres,
#                        the templates and the five fold_squash_* proof
#                        databases must be empty, or the script refuses.
#                        USER and PASS are percent-encoded, HOST and PORT are
#                        explicit, and the only query keys accepted are sslmode
#                        and connect_timeout (a database, service, host, user,
#                        options or similar key could silently redirect the
#                        proof). Every libpq variable (PG*) in the environment
#                        is cleared for every command the script runs.
#   --client-url URL     The same server as psql and pg_dump reach it (they may
#                        run in a container). Default: --server-url. The same
#                        rules apply. Before the script changes anything it
#                        proves that both URLs reach the same cluster, the
#                        database postgres and the same superuser role: the
#                        Go driver (the one the Fold binaries use) holds an
#                        advisory lock that the wrapped psql must find taken,
#                        and both report the same system identifier, start
#                        time and role. If that cannot be proved the script
#                        stops with the database untouched.
#   --evidence-dir DIR   New or empty directory for the dumps, the stored goose
#                        rows, the diff and the summary. Required.
#   --psql WORD          Append one word to the psql command. Repeat it for a
#                        wrapper, for example: --psql docker --psql exec
#                        --psql -i --psql CONTAINER --psql psql. The first use
#                        replaces the default "psql".
#   --pg-dump WORD       The same for pg_dump. The script appends its own
#                        arguments; the wrapper is never run through eval.
#   --recreate           Drop and recreate the proof databases and roles if
#                        they exist. Without it, a proof database that exists
#                        must already be truly empty.
#   --boot-port N        Port for the fresh-boot server (default 18943). It must
#                        be free: the script refuses a port that already has a
#                        listener (and leaves that listener alone), and counts a
#                        health answer only while its own child owns the port.
#   generate only:
#     --install          After the whole proof passes, replace the migration
#                        chain in this checkout with the generated baseline.
#                        Without it, the repository is never modified.
#   proof only:
#     --candidate-tree D Tree to test (default: this checkout).
#     --expect-generated Also require the candidate baseline to equal, byte for
#                        byte, what the generator makes from the frozen chain
#                        (the S2 proof; S3 and S4 edit the file on purpose).
#     --allow-diff       Record a non-empty dump diff and exit 0 (S3 and S4
#                        expect hunks; their allow-lists are checked by hand).
#
# Needs: bash, git, tar, go, awk, diff, cmp, sha256sum, curl, ss, env, grep, psql and pg_dump
# whose major version equals the server's. Everything runs in a temporary
# directory; the repository changes only under --install, which swaps the
# migrations directory in two renames.
#
# Wrapping psql and pg_dump in docker, shown for a scratch container named
# scratch-pg whose port 5432 is published on 127.0.0.1:55432:
#   scripts/baseline-proof.sh generate \
#     --server-url postgres://postgres:secret@127.0.0.1:55432 \
#     --client-url postgres://postgres:secret@127.0.0.1:5432 \
#     --psql docker --psql exec --psql scratch-pg --psql psql \
#     --pg-dump docker --pg-dump exec --pg-dump scratch-pg --pg-dump pg_dump \
#     --evidence-dir /tmp/s2-evidence --recreate --install
set -euo pipefail
shopt -s inherit_errexit

# ---------------------------------------------------------------------------
# The frozen source. Generation is bound to this commit and to this manifest of
# the 63 migration files (versions 1-64; there is no 00021). A mismatch of the
# archive, or of the working tree before --install, stops the script.
# ---------------------------------------------------------------------------
readonly FROZEN_SHA=be61e582c3011263307e4db4497056f7fd70bfd9
readonly FROZEN_FILES=63
readonly FROZEN_HEAD=64
readonly GOOSE_VERSION=v3.29.0

frozen_manifest() {
	cat <<'MANIFEST'
c8067b1bf34b193a58eff216a5778ca0be3cfe7e3d717a7ee76347d2e5e73e28  migrations/00001_baseline.sql
748f8ff49e5a810365b724db2aaad450d21eec4622b4a67ed310601184ae89a9  migrations/00002_visibility_and_audit_log.sql
08e70ed6a8679e86939ffbd9b58a00d14d43ff87ccafc34272bcbd3e53dd01b0  migrations/00003_email_login.sql
e5f53b38694b22a398d26e3a2ff2ce32b5c4fd51626c9f6888fa711279079cc3  migrations/00004_group_member_limit.sql
2f2c990f7a048143c9bdf67ec90744d2a42f4ffd48a57554e7eec74cd81d598b  migrations/00005_events.sql
3833759b2d65dc9673c85ab8f16a21ca967e73655fc8ce5e6096d348de4853c4  migrations/00006_event_visibility.sql
5ce7343b7c5814afe53f1eb3abf5d6696ef52557e9bd0d1e0b33c543d321a1f9  migrations/00007_event_description.sql
63ec763496f02d77f77e185201a404a1f4711700ff5ef905ac3ecd9a8dcd6639  migrations/00008_drop_pending_return.sql
2216d3e1111def898803d13aecb5d557305ca5282689c3bb9383ee5fd864ccbd  migrations/00009_event_visibility_own.sql
1c0ea1616b8ad50070c1ed1e82a260081ca55d55175f65eb6d241f4ceae87266  migrations/00010_notifications.sql
4492d605197acb92fb79681e429ca499c5c5c8d17c7f280b98a4653ce2e758fe  migrations/00011_m1_2_email.sql
1a5a65c9bf00172ae5192dca6ba751e2bddf0c23d8bab1d7b98d093319f0d440  migrations/00012_m1_3_account_requests.sql
66fb936d8f391faac9f85a208992ebb42e08d5ff212503d2f8575d44ff64f63b  migrations/00013_upload_only_covers.sql
40952b2d0364c615879e1840db04bef5e988307c287277de528662175febf59d  migrations/00014_guest_registration.sql
a0e07232c1bc7520c9eb86c16fc46f7c4b4aa6ec94642ab9cb040d104772fda5  migrations/00015_capacity_waitlist.sql
491c05343e24dccfcf001f83100255c969840b36438290360fcb6221ee9df8f7  migrations/00016_user_timezone.sql
bcc06db27af8b7c311b7d83c11ca738d11fbf2a3c4204e9cda62c938e7a4f083  migrations/00017_calendar_tokens.sql
05c09940c9a585d215f28f89052909888554c270f799f202882cb5e10b1bd56c  migrations/00018_registrations_manage_token_index.sql
73c5614cd23c1142a5485af355dbc8cf62cf6364d4903f274e034eb2bedf0d6b  migrations/00019_video_links.sql
3f1fe35bfbd639ecd2359a89c2333b0694f3fa544a7d97ece0598596e9fe05a9  migrations/00020_remote_attendance.sql
d465f37547d727339eea2d3e0620206955b8115efd1efe82a1a30c19cbd02dd3  migrations/00022_households.sql
312f66d9c826d8ae24ba1d83e27935eff7eff857739afe4e67e632050873b78e  migrations/00023_childcare.sql
3cf6acdd92a833d34649a6622fabbcc7d573beef0a212185c98dd31f79250cc8  migrations/00024_household_registrations.sql
afb67287c1869733555bc20ac82637b54c7f09a21c0b1ad55a04ec5fab1426b3  migrations/00025_household_conversion.sql
d668ff96d55facbeaa99aa78f021090821c3bc0d7034b348b859f042215b1001  migrations/00026_teen_consent.sql
853d38c73e3fb318b1fb489b57048af63d3f5c185a9e8000f64e8f0b7675b980  migrations/00027_teen_guardians.sql
a1e03a086c6756c1d187314508bfe504934a51170fb577bba5c4ced5212d33b9  migrations/00028_staged_teen_conversion.sql
0c529dc01568f3259c9daaa8996d62729209084583a2f0c7be3f17c99c917f46  migrations/00029_contact_privacy.sql
52df22dcdf0c85358ca38dc24a37a21843f5ab1ff6508a49f88ecd76fcc00606  migrations/00030_contact_requests.sql
08b91b1bb43ba809edeea632eff3f5035044823bd0fe1cedd2a42b2c1708d764  migrations/00031_contact_add_requests.sql
6203dd2ac38730bfce212ba026c899e634bfc169b84e7f6341521a9493d38864  migrations/00032_occurrence_visibility.sql
3db69f71e1a8afdd3b42c0b252c620a77044531e983d89adf14cadebf9fbe8b2  migrations/00033_series_undo_receipts.sql
dc54dec94e51ad31b3857295cd709aaae153038c5acf75f3cfb20e2718b1b80e  migrations/00034_occurrence_access_departure_origin.sql
9fe3397a7e0ed173b4c276566d2427001b3f61bce246de5d6139dda177502a4a  migrations/00035_departure_occurrence_access_expiry.sql
a6ca3486892fd1139f54951b61f7e8d43cb59c7ebe9c44dfc91d9cd7279ee07a  migrations/00036_membership_tenure.sql
1a76b6ba1c50be7c47a21f114985a6fd1d309193206d859372751a6d81abbea3  migrations/00037_departure_access_supersession.sql
976760d224c9ef72396d48cfb359797dde1d479ce38b6c1227f6d1b586c40086  migrations/00038_occurrence_access_active_occurrence.sql
3643a41553427ba2af13d5319d6d567c2a22ab9a833e3f046bd7c70c1d20072b  migrations/00039_membership_left.sql
24ed0d790c721b4935956d1c43ea2670cf6611df99d2b2125df395741dc185b3  migrations/00040_email_change_cancel.sql
a515af806436c3de9cf9df2b1baea14f659c640141f1143bc88b5b92a9fa8c54  migrations/00041_memberships_group_id.sql
09e79dedeff22d3a14b977fa7d3725c9df0f69d10c5d1eebcd537c3380c2faf5  migrations/00042_registrations_user_id.sql
ea2e69757e75d5ac2b72e576dfeade7a34d086a8f08a59501b2dbe0c9a8856be  migrations/00043_occurrence_cancel_source.sql
30499c5b74600c7ee86a9c24d90f145359f7380f95b4f865889e15a6c951bb8c  migrations/00044_consents.sql
47d9f7c1ee36f97894780fbb41f1679df8c8db272728b3843bab8e37ab1a3ffd  migrations/00045_rooms.sql
1a604ccea22bc1b2da61a3bc15eba4676a0b539c76e4958b554863700f9a1a20  migrations/00046_room_bookings.sql
bf3baf08dcef57049c0cb6276749b5bf91b452acb5e1ffc05c01a0f63478d775  migrations/00047_event_default_room.sql
91632d998d2c53b49a39f6eafa58b473e48cc4a3ba52a02babc492d9b206dd50  migrations/00048_room_requests.sql
9b28cb429af0f92ef33477e5cf6d1f6136e08cd0e4e4a042780d1f14d8fff707  migrations/00049_room_feed_tokens.sql
aaaba2ce64b4cdb0e035352cbbff5e1451b175fe5c2c03f5c8915013e26ef01e  migrations/00050_room_bookings_confirmed_range.sql
d013874a5028ce412f8b8e5e2a3803fb9a6843c7ca318b5489e95663cb8ebe19  migrations/00051_jobs.sql
731e8a083801b5013fa48122c370ab31f03c1d4c83a6063961ef3fc127d9c51e  migrations/00052_suppressions.sql
37863f744323f01fbe6dba51aa1b3d745a75871dd67f64d228931ad29a2cf34d  migrations/00053_outbox_list_unsubscribe.sql
bdff9213b2ba7242c1e26f8af0962a2869791e0b96b3c0ae7a0498dc1645dca9  migrations/00054_messages.sql
4168ecb2acda8d778b085878dbcff292a9101ff1a4807c60fc129a9c75f3b693  migrations/00055_outbox_html.sql
a08e9d9879dbd9e7fc74fe3f8e0cde82f085dc4ed7ddc3be0482b2a7344684a5  migrations/00056_message_recipients.sql
23a4a3bd03a2cab5b153954ac1e9a0463e68741ff08a1f5bd7951560d2646b8d  migrations/00057_reminders.sql
5f1d4cecd946aa9930383ca02fdf60935dc69ab223ac7040829663f166f0e778  migrations/00058_push_subscriptions.sql
8f629366381fec75e5b23e665d4d1cf4e04a7a5ecee277e9501e5cc587457903  migrations/00059_push_text.sql
90c9c00416e7221e63910352b1ef0ebceac4b6be59fe5084e7c679e114c724be  migrations/00060_push_hourly_limit.sql
621c8bdc83de9fa483586baec27dcdb87178765358abfc9437e760d5eaeb708f  migrations/00061_message_push.sql
edecc13987943d4e61c3bc9ba1559ee1697479f576b9a9ad176cd5bfc53661a5  migrations/00062_reminder_push.sql
ca14fd3cf0ec35903ea9d4b75def5d9f82d8397bb9a7e9d2558d504d6ac3a33b  migrations/00063_logout_version.sql
27e20e496925b6994327e89ee33d73f7ac18250736ed817d97d1a1ea03dd4170  migrations/00064_notification_delivery_outbox.sql
MANIFEST
}

readonly DB_OLD=fold_squash_old
readonly DB_NEW=fold_squash_new
readonly DB_OWNER=fold_squash_owner
readonly DB_BOOT=fold_squash_boot
readonly DB_CHAIN=fold_squash_chain
readonly ROLE_EXTOWNER=fold_squash_extowner
readonly ROLE_MIGRATOR=fold_squash_migrator

# What a baseline-only database stores in goose_db_version: goose's initial
# version 0 row, then version 1 applied, in insertion order. `migrate status`
# cannot show this: it also reports version 1 applied on an old-chain database.
readonly BASELINE_ROWS=$'0|t\n1|t'
readonly BASELINE_STATUS='[{"version":1,"name":"00001_baseline.sql","state":"applied"}]'

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
readonly REPO

MODE=""
SERVER_URL=""
CLIENT_URL=""
EVIDENCE=""
RECREATE=0
INSTALL=0
ALLOW_DIFF=0
EXPECT_GENERATED=0
CANDIDATE_TREE=""
BOOT_PORT=18943
PSQL=(psql)
PSQL_SET=0
PGDUMP=(pg_dump)
PGDUMP_SET=0

TMP=""
IDENT_BIN=""
ROUTE_IDENTITY=""
# env -u options for every libpq variable in the caller's environment (see
# collect_pg_env); every connection-making command runs under them.
PG_UNSET=()
BOOT_PID=""
ROLES_CREATED=0
OLD_BIN=""
NEW_BIN=""
CANDIDATE_SQL=""

say() { printf 'baseline-proof: %s\n' "$*"; }
die() {
	printf 'baseline-proof: %s\n' "$*" >&2
	exit 1
}
usage() {
	sed -n '2,/^set -euo pipefail/p' "${BASH_SOURCE[0]}" | head -n -1 | sed 's/^# \{0,1\}//' >&2
	exit 2
}

cleanup() {
	local rc=$?
	trap - EXIT
	if [[ -n $BOOT_PID ]]; then
		kill "$BOOT_PID" 2>/dev/null || true
		wait "$BOOT_PID" 2>/dev/null || true
	fi
	if ((ROLES_CREATED)); then
		drop_owner_fixture >/dev/null 2>&1 || true
	fi
	if [[ -n $TMP && $TMP == */baseline-proof.* && -d $TMP ]]; then
		rm -rf -- "$TMP"
	fi
	exit "$rc"
}

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
parse_args() {
	(($# >= 1)) || usage
	MODE=$1
	shift
	[[ $MODE == generate || $MODE == proof ]] || usage
	while (($# > 0)); do
		case $1 in
		--server-url | --client-url | --evidence-dir | --psql | --pg-dump | --boot-port | --candidate-tree)
			(($# >= 2)) || die "$1 needs a value"
			case $1 in
			--server-url) SERVER_URL=$2 ;;
			--client-url) CLIENT_URL=$2 ;;
			--evidence-dir) EVIDENCE=$2 ;;
			--boot-port) BOOT_PORT=$2 ;;
			--candidate-tree) CANDIDATE_TREE=$2 ;;
			--psql)
				if ((PSQL_SET == 0)); then
					PSQL=()
					PSQL_SET=1
				fi
				PSQL+=("$2")
				;;
			--pg-dump)
				if ((PGDUMP_SET == 0)); then
					PGDUMP=()
					PGDUMP_SET=1
				fi
				PGDUMP+=("$2")
				;;
			esac
			shift 2
			;;
		--recreate)
			RECREATE=1
			shift
			;;
		--install)
			[[ $MODE == generate ]] || die "--install belongs to generate"
			INSTALL=1
			shift
			;;
		--allow-diff | --expect-generated)
			[[ $MODE == proof ]] || die "$1 belongs to proof"
			if [[ $1 == --allow-diff ]]; then ALLOW_DIFF=1; else EXPECT_GENERATED=1; fi
			shift
			;;
		-h | --help) usage ;;
		*) die "unknown argument: $1 (see --help)" ;;
		esac
	done
	[[ -n $SERVER_URL ]] || die "--server-url is required"
	[[ -n $EVIDENCE ]] || die "--evidence-dir is required"
	[[ -n $CLIENT_URL ]] || CLIENT_URL=$SERVER_URL
	[[ $BOOT_PORT =~ ^[0-9]{2,5}$ ]] || die "--boot-port must be a port number"
	[[ $MODE == proof || -z $CANDIDATE_TREE ]] || die "--candidate-tree belongs to proof"
	split_url "$SERVER_URL" || die "--server-url must look like postgres://user:pass@host:port with no database path (percent-encode user and password, give host and port explicitly)"
	split_url "$CLIENT_URL" || die "--client-url must look like postgres://user:pass@host:port with no database path (percent-encode user and password, give host and port explicitly)"
	if [[ $MODE == proof ]]; then
		[[ -n $CANDIDATE_TREE ]] || CANDIDATE_TREE=$REPO
		[[ -d $CANDIDATE_TREE ]] || die "--candidate-tree is not a directory"
		CANDIDATE_TREE=$(cd "$CANDIDATE_TREE" && pwd)
	fi
}

# ---------------------------------------------------------------------------
# URLs and SQL
# ---------------------------------------------------------------------------
# A base URL has exactly this shape, so that taking it apart and putting it
# back together (db_url, user_url) cannot disagree with the Go driver or libpq
# about where the user, host, port and query end: percent-encoded user and
# optional password, one host and one port written out, no path, and a query of
# plain key=value pairs. The query keys are checked by the identity helper.
readonly URL_RE='^(postgres|postgresql)://([A-Za-z0-9._~%-]+)(:([A-Za-z0-9._~%-]*))?@(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9._-]+):([0-9]{1,5})/?(\?([A-Za-z0-9._~%=&-]+))?$'
URL_SCHEME="" URL_USERINFO="" URL_HOSTPORT="" URL_QUERY=""

# split_url BASE: set URL_SCHEME, URL_USERINFO (user and ":pass" as written),
# URL_HOSTPORT and URL_QUERY ("?..." or empty); fail on any other shape.
split_url() {
	[[ $1 =~ $URL_RE ]] || return 1
	local port=${BASH_REMATCH[6]}
	((10#$port >= 1 && 10#$port <= 65535)) || return 1
	URL_SCHEME=${BASH_REMATCH[1]}
	URL_USERINFO=${BASH_REMATCH[2]}${BASH_REMATCH[3]}
	URL_HOSTPORT=${BASH_REMATCH[5]}:${BASH_REMATCH[6]}
	URL_QUERY=${BASH_REMATCH[7]}
}

# db_url BASE NAME: BASE with the database path set to NAME.
db_url() {
	split_url "$1" || die "internal error: a base URL changed shape"
	printf '%s://%s@%s/%s%s' "$URL_SCHEME" "$URL_USERINFO" "$URL_HOSTPORT" "$2" "$URL_QUERY"
}

# user_url BASE USER PASS NAME: BASE signed in as USER, database NAME.
user_url() {
	split_url "$1" || die "internal error: a base URL changed shape"
	printf '%s://%s:%s@%s/%s%s' "$URL_SCHEME" "$2" "$3" "$URL_HOSTPORT" "$4" "$URL_QUERY"
}

server_db() { db_url "$SERVER_URL" "$1"; }
client_db() { db_url "$CLIENT_URL" "$1"; }

# collect_pg_env: every libpq variable (PGHOST, PGDATABASE, PGUSER, PGSERVICE,
# PGOPTIONS, ...) in this environment would be a second, silent source of
# routing and role for psql, pg_dump and the Fold binaries alike. Each command
# below runs with all of them removed and with no password file, so only the
# URLs decide where a connection goes.
collect_pg_env() {
	local name
	PG_UNSET=()
	while IFS= read -r name; do
		PG_UNSET+=(-u "$name")
	done < <(compgen -e | { grep '^PG' || true; })
}

# psql_at DBURL SQL: run one command, print bare values separated by |.
psql_at() {
	env "${PG_UNSET[@]}" PGPASSFILE=/dev/null \
		"${PSQL[@]}" -X -q -A -t -F '|' -v ON_ERROR_STOP=1 --dbname "$1" -c "$2"
}

admin_sql() { psql_at "$(client_db postgres)" "$1"; }

db_exists() { [[ $(admin_sql "SELECT count(*) FROM pg_database WHERE datname = '$1'") == 1 ]]; }

# emptiness URL: 0 when the database holds no relation, function, user type,
# extension (other than plpgsql) or schema of its own; goose's table counts.
emptiness() {
	psql_at "$1" "SELECT
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		    AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%')
		+ (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		    WHERE n.nspname NOT IN ('pg_catalog', 'information_schema'))
		+ (SELECT count(*) FROM pg_extension WHERE extname <> 'plpgsql')
		+ (SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
		    WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		      AND n.nspname NOT LIKE 'pg_toast%' AND t.typtype IN ('d', 'e', 'r', 'm'))
		+ (SELECT count(*) FROM pg_namespace
		    WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'public')
		      AND nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp%')"
}

# empty_db NAME: leave NAME existing and truly empty, or stop.
empty_db() {
	local name=$1
	if db_exists "$name"; then
		if ((RECREATE)); then
			admin_sql "DROP DATABASE $name WITH (FORCE)" >/dev/null
		else
			[[ $(emptiness "$(client_db "$name")") == 0 ]] ||
				die "database $name exists and is not empty; pass --recreate to drop it (a proof database only)"
			return 0
		fi
	fi
	admin_sql "CREATE DATABASE $name" >/dev/null
}

# scratch_server_check: the server may hold only postgres, the templates, the
# proof databases and databases with nothing in them.
scratch_server_check() {
	local superuser name other
	superuser=$(admin_sql "SELECT rolsuper FROM pg_roles WHERE rolname = current_user")
	[[ $superuser == t ]] || die "the --server-url role must be a superuser on a scratch server"
	while IFS= read -r name; do
		[[ -n $name ]] || continue
		[[ $name =~ ^[A-Za-z0-9_]+$ ]] || die "the server holds a database with an unexpected name; not a scratch server"
		case $name in
		postgres | "$DB_OLD" | "$DB_NEW" | "$DB_OWNER" | "$DB_BOOT" | "$DB_CHAIN") continue ;;
		esac
		other=$(emptiness "$(client_db "$name")")
		[[ $other == 0 ]] || die "the server holds a database named $name with objects in it; this is not a scratch server. Refusing to touch it."
	done < <(admin_sql "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY datname")
}

stored_rows() { psql_at "$(client_db "$1")" "SELECT version_id, is_applied FROM goose_db_version ORDER BY id"; }

# require_baseline_rows DB: exactly goose's version 0 row and version 1 applied.
require_baseline_rows() {
	local rows
	rows=$(stored_rows "$1") || die "cannot read goose_db_version in $1"
	[[ $rows == "$BASELINE_ROWS" ]] ||
		die "goose_db_version in $1 holds [$(tr '\n' ' ' <<<"$rows")], want exactly version 0 and version 1 applied"
	printf '%s\n' "$rows" >"$EVIDENCE/stored-versions-$1.txt"
}

# ---------------------------------------------------------------------------
# Frozen source and tool checks
# ---------------------------------------------------------------------------
require_tools() {
	local t
	for t in git tar go awk diff cmp sha256sum curl ss env grep mktemp find sort xargs; do
		command -v "$t" >/dev/null 2>&1 || die "$t is required"
	done
	command -v "${PSQL[0]}" >/dev/null 2>&1 || die "${PSQL[0]} (psql command) was not found"
	command -v "${PGDUMP[0]}" >/dev/null 2>&1 || die "${PGDUMP[0]} (pg_dump command) was not found"
}

# manifest_of DIR: sha256 of every DIR/migrations/*.sql, sorted bytewise.
manifest_of() {
	(cd "$1" && LC_ALL=C find migrations -maxdepth 1 -type f -name '*.sql' -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum)
}

# require_frozen_manifest DIR WHAT: DIR/migrations is exactly the frozen chain.
require_frozen_manifest() {
	local got
	got=$(manifest_of "$1")
	if [[ $got != "$(frozen_manifest)" ]]; then
		diff -u <(frozen_manifest) <(printf '%s\n' "$got") >&2 || true
		die "$2 does not match the frozen migration manifest (commit $FROZEN_SHA, $FROZEN_FILES files, head $FROZEN_HEAD)"
	fi
}

check_manifest_shape() {
	local count last
	count=$(frozen_manifest | wc -l)
	((count == FROZEN_FILES)) || die "the embedded manifest lists $count files, want $FROZEN_FILES"
	last=$(frozen_manifest | tail -n 1 | sed -E 's/.*migrations\/0*([0-9]+)_.*/\1/')
	((last == FROZEN_HEAD)) || die "the embedded manifest ends at version $last, want $FROZEN_HEAD"
}

# extract_frozen DEST: the frozen commit's tree, by git archive. No worktree.
extract_frozen() {
	mkdir -p "$1"
	git -C "$REPO" archive --format=tar "$FROZEN_SHA" | tar -x -C "$1"
}

require_frozen_commit() {
	local got
	got=$(git -C "$REPO" rev-parse --verify --quiet "${FROZEN_SHA}^{commit}") ||
		die "the frozen commit $FROZEN_SHA is not in this repository (fetch full history)"
	[[ $got == "$FROZEN_SHA" ]] || die "frozen commit resolves to $got"
}

# The goose version table is made and owned by goose at run time, so the
# baseline must not create it (section 5.2). Check the pinned provider says so.
verify_goose_provider() {
	local tree=$1 info version dir
	grep -qF "github.com/pressly/goose/v3 $GOOSE_VERSION" "$tree/go.mod" ||
		die "the frozen go.mod does not pin goose $GOOSE_VERSION"
	info=$(cd "$tree" && GOFLAGS=-mod=readonly go list -m -f '{{.Version}} {{.Dir}}' github.com/pressly/goose/v3) ||
		die "cannot resolve the pinned goose module (is the module cache populated?)"
	version=${info%% *}
	dir=${info#* }
	[[ $version == "$GOOSE_VERSION" ]] || die "goose resolves to $version, want $GOOSE_VERSION"
	grep -qF 'CREATE TABLE %s' "$dir/internal/dialects/postgres.go" ||
		die "goose $GOOSE_VERSION no longer creates its own version table; recheck section 5.2"
}

build_fold() { # TREE OUT
	(cd "$1" && CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath -buildvcs=false -o "$2" .)
}

# fold_migrate BIN DBNAME ARGS...
fold_migrate() {
	local bin=$1 name=$2
	shift 2
	env "${PG_UNSET[@]}" -u APP_ENV -u SESSION_SECRET PGPASSFILE=/dev/null "DATABASE_URL=$(server_db "$name")" "$bin" migrate "$@"
}

# ---------------------------------------------------------------------------
# Connection identity (S2-R1)
# ---------------------------------------------------------------------------
# psql, pg_dump and every administrative statement go through --client-url; the
# Fold binaries go through --server-url. Nothing else ties the two together, so
# before the first database change this helper proves they reach the same
# cluster, the database postgres and the same superuser role. It is a small Go
# program built inside the extracted frozen tree (never the repository), so it
# parses URLs and connects with the pinned pgx driver exactly as `fold migrate`
# (sql.Open "pgx") and the server (GORM's pgx dialector) do, and it runs the
# wrapped psql by argument, never through eval. The scratch dumps and the Fold
# binaries then use only URLs of this proved shape.
read -r -d '' IDENT_GO <<'GO' || true
// Command proofident proves that the Go driver route and the wrapped psql
// route of scripts/baseline-proof.sh reach one cluster, one database and one
// role. It prints only non-secret facts; every error is scrubbed of the URLs
// and passwords it was given.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const wantDB = "postgres"

// Identical in spirit to URL_RE in the script, with the database path required.
var routeRE = regexp.MustCompile(`^(?:postgres|postgresql)://([A-Za-z0-9._~%-]+)(?::([A-Za-z0-9._~%-]*))?@(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9._-]+):([0-9]{1,5})/([A-Za-z0-9_]+)(?:\?([A-Za-z0-9._~%=&-]+))?$`)

const identitySQL = `SELECT concat_ws('|', current_database(), session_user, current_user, ` +
	`(SELECT rolsuper::text FROM pg_roles WHERE rolname = session_user), ` +
	`(SELECT system_identifier::text FROM pg_control_system()), ` +
	`to_char(pg_postmaster_start_time() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'), ` +
	`current_setting('server_version_num'))`

var secrets []string

type route struct {
	user, host string
	port       int
	cfg        *pgx.ConnConfig
}

func scrub(s string) string {
	pairs := []string{}
	for _, v := range secrets {
		if v != "" {
			pairs = append(pairs, v, "***")
		}
	}
	if len(pairs) == 0 {
		return s
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

func noteSecrets(raw string) {
	secrets = append(secrets, raw)
	if m := regexp.MustCompile(`://[^:@/]*:([^@/]*)@`).FindStringSubmatch(raw); m != nil && m[1] != "" {
		secrets = append(secrets, m[1])
		if d, err := url.PathUnescape(m[1]); err == nil {
			secrets = append(secrets, d)
		}
	}
}

// parseRoute accepts only the exact shape the script builds, then lets the real
// driver parse it and requires that the driver read back the same host, port,
// user, password and database: a query key that overrides any of them, an
// alias such as dbname, a service, options or a second host would change what
// the driver resolved and is refused, as is anything the environment added.
func parseRoute(label, raw string) (*route, error) {
	m := routeRE.FindStringSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("the %s URL is not postgres://user[:pass]@host:port/%s[?sslmode=...] with an explicit user, host and port", label, wantDB)
	}
	user, err := url.PathUnescape(m[1])
	if err != nil || user == "" {
		return nil, fmt.Errorf("the %s URL has an invalid user", label)
	}
	pass, err := url.PathUnescape(m[2])
	if err != nil {
		return nil, fmt.Errorf("the %s URL has an invalid password", label)
	}
	if m[5] != wantDB {
		return nil, fmt.Errorf("the %s URL names database %q, want %q", label, m[5], wantDB)
	}
	port, err := strconv.Atoi(m[4])
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("the %s URL has an invalid port", label)
	}
	if q := m[6]; q != "" {
		seen := map[string]bool{}
		for _, pair := range strings.Split(q, "&") {
			rk, rv, ok := strings.Cut(pair, "=")
			if !ok || strings.Contains(rv, "=") {
				return nil, fmt.Errorf("the %s URL has a malformed query parameter", label)
			}
			key, kerr := url.PathUnescape(rk)
			val, verr := url.PathUnescape(rv)
			if kerr != nil || verr != nil {
				return nil, fmt.Errorf("the %s URL has an undecodable query parameter", label)
			}
			if seen[key] {
				return nil, fmt.Errorf("the %s URL repeats the query parameter %q", label, key)
			}
			seen[key] = true
			switch key {
			case "sslmode":
				switch val {
				case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
				default:
					return nil, fmt.Errorf("the %s URL has an unknown sslmode", label)
				}
			case "connect_timeout":
				if n, nerr := strconv.Atoi(val); nerr != nil || n < 0 {
					return nil, fmt.Errorf("the %s URL has an invalid connect_timeout", label)
				}
			default:
				return nil, fmt.Errorf("the %s URL sets the query parameter %q; only sslmode and connect_timeout are accepted, because any other key (a database, service, host, user or options) can redirect the proof", label, key)
			}
		}
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("the Go driver cannot parse the %s URL: %v", label, err)
	}
	host := strings.Trim(m[3], "[]")
	if cfg.Host != host || int(cfg.Port) != port || cfg.User != user || cfg.Password != pass || cfg.Database != m[5] {
		return nil, fmt.Errorf("the Go driver resolved the %s URL to a different host, port, user, password or database than it names", label)
	}
	for _, f := range cfg.Fallbacks {
		if f.Host != cfg.Host || f.Port != cfg.Port {
			return nil, fmt.Errorf("the %s URL resolves to more than one server", label)
		}
	}
	if len(cfg.RuntimeParams) != 0 {
		return nil, fmt.Errorf("the %s URL carries session settings the proof did not ask for", label)
	}
	return &route{user: user, host: m[3], port: port, cfg: cfg}, nil
}

func checkEnv() error {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PG") && kv != "PGPASSFILE=/dev/null" {
			return fmt.Errorf("environment variable %s is set; libpq variables could redirect a connection, so the proof clears them", name)
		}
	}
	return nil
}

func psql(ctx context.Context, words []string, dburl, sql string) (string, error) {
	args := append(append([]string{}, words[1:]...), "-X", "-q", "-A", "-t", "-F", "|", "-v", "ON_ERROR_STOP=1", "--dbname", dburl, "-c", sql)
	cmd := exec.CommandContext(ctx, words[0], args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PROOF_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(errb.String()), "\n")
		return "", fmt.Errorf("psql failed: %v: %s", err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func run(words []string) error {
	serverURL, clientURL := os.Getenv("PROOF_SERVER_URL"), os.Getenv("PROOF_CLIENT_URL")
	noteSecrets(serverURL)
	noteSecrets(clientURL)
	if len(words) == 0 {
		return errors.New("no psql command was given")
	}
	if err := checkEnv(); err != nil {
		return err
	}
	srv, err := parseRoute("--server-url", serverURL)
	if err != nil {
		return err
	}
	cli, err := parseRoute("--client-url", clientURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	db := stdlib.OpenDB(*srv.cfg)
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("the Go driver cannot connect with --server-url: %v", err)
	}
	defer conn.Close()
	var goID string
	if err := conn.QueryRowContext(ctx, identitySQL).Scan(&goID); err != nil {
		return fmt.Errorf("the Go route cannot read the cluster identity (the role must be a superuser): %v", err)
	}
	f := strings.Split(goID, "|")
	if len(f) != 7 {
		return errors.New("the Go route returned an unreadable identity")
	}
	if f[0] != wantDB || f[1] != srv.user || f[2] != srv.user {
		return errors.New("the Go route is not the database postgres as the role named in --server-url (session_user and current_user must both be that role)")
	}
	if f[3] != "true" {
		return errors.New("the --server-url role is not a superuser")
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	key := int64(binary.BigEndian.Uint64(b[:]) >> 1)
	var held bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1::bigint)", key).Scan(&held); err != nil || !held {
		return errors.New("the Go route could not take the proof's advisory lock")
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1::bigint)", key)
	}()

	probe, err := psql(ctx, words, clientURL, fmt.Sprintf("SELECT pg_try_advisory_lock(%d)", key))
	if err != nil {
		return fmt.Errorf("the --client-url route cannot reach the server: %v", err)
	}
	if probe != "f" {
		return errors.New("the --client-url route did not find the lock the Go route holds: it reaches a different cluster or database than --server-url")
	}
	cliID, err := psql(ctx, words, clientURL, identitySQL)
	if err != nil {
		return fmt.Errorf("the --client-url route cannot read the cluster identity: %v", err)
	}
	if cliID != goID {
		return errors.New("the --client-url route reports a different database, role, cluster or start time than the Go route")
	}
	fmt.Println("connection identity: verified before any change")
	fmt.Printf("database: %s\n", f[0])
	fmt.Printf("session and current role: %s (superuser)\n", f[1])
	fmt.Printf("cluster system identifier: %s\n", f[4])
	fmt.Printf("postmaster start (UTC): %s\n", f[5])
	fmt.Printf("server_version_num: %s\n", f[6])
	fmt.Printf("go route (--server-url): %s:%d\n", srv.host, srv.port)
	fmt.Printf("client route (--client-url): %s:%d\n", cli.host, cli.port)
	fmt.Println("proof: the Go driver held an advisory lock in postgres that the wrapped psql found taken, and both routes returned the same identity")
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "baseline-proof: %s\n", scrub(err.Error()))
		os.Exit(1)
	}
}
GO

build_ident() { # TREE: build the helper inside the extracted frozen tree only
	mkdir -p "$1/proofident"
	printf '%s\n' "$IDENT_GO" >"$1/proofident/main.go"
	IDENT_BIN=$TMP/proofident
	(cd "$1" && CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath -buildvcs=false -o "$IDENT_BIN" ./proofident) ||
		die "cannot build the connection-identity helper with the pinned driver"
}

# The helper is built against the frozen go.mod. In proof mode the Fold
# binaries come from the candidate tree, so its driver pins must match.
driver_pins() { # TREE
	awk '$1 == "github.com/jackc/pgx/v5" || $1 == "gorm.io/driver/postgres" { print $1, $2 }' "$1/go.mod" | sort
}

require_same_driver() { # CANDIDATE_TREE
	local want got
	want=$(driver_pins "$TMP/frozen")
	got=$(driver_pins "$1")
	[[ -n $want && $want == "$got" ]] ||
		die "the candidate tree pins a different pgx or GORM Postgres driver than the frozen tree; the connection-identity proof would not describe the candidate's connections"
}

# verify_routes: before the first database change, prove that --server-url (the
# Go driver, as the Fold binaries connect) and --client-url (the wrapped psql
# and pg_dump) reach the same cluster, the database postgres and the same
# superuser role. Fails closed; prints and records only non-secret identity.
verify_routes() {
	say "connection identity: proving both URLs reach one cluster, database and role"
	ROUTE_IDENTITY=$(env "${PG_UNSET[@]}" PGPASSFILE=/dev/null \
		"PROOF_SERVER_URL=$(server_db postgres)" "PROOF_CLIENT_URL=$(client_db postgres)" \
		"$IDENT_BIN" "${PSQL[@]}") ||
		die "--server-url and --client-url were not proved to reach the same cluster, database and role; nothing was changed"
}

# ---------------------------------------------------------------------------
# pg_dump, normalization and the generator (one awk program, two modes)
# ---------------------------------------------------------------------------
check_dump_major() { # DB
	local ver major num
	ver=$(env "${PG_UNSET[@]}" PGPASSFILE=/dev/null "${PGDUMP[@]}" --version) || die "pg_dump --version failed"
	ver=${ver#*) }
	major=${ver%%.*}
	[[ $major =~ ^[0-9]+$ ]] || die "cannot read the pg_dump major version from [$ver]"
	num=$(psql_at "$(client_db "$1")" "SHOW server_version_num")
	[[ $num =~ ^[0-9]+$ ]] || die "cannot read server_version_num of $1"
	((num / 10000 == major)) ||
		die "pg_dump is major $major but the server holding $1 is major $((num / 10000)); a different major rewords defaults and breaks the diff"
}

dump_db() { # DB OUTFILE
	check_dump_major "$1"
	env "${PG_UNSET[@]}" PGPASSFILE=/dev/null \
		"${PGDUMP[@]}" --schema-only --no-owner --no-privileges --dbname "$(client_db "$1")" >"$2" ||
		die "pg_dump of $1 failed"
	grep -q '^-- PostgreSQL database dump complete' "$2" || die "the dump of $1 is incomplete"
}

read -r -d '' DUMP_AWK <<'AWK' || true
# Splits a pg_dump into the sections it announces with a "-- Name: ...; Type:"
# header, and in "clean" mode drops what section 5.2 lists. In "generate" mode
# it also writes the goose migration. Anything it does not recognize stops it.
function fail(msg) { print "baseline-proof: " msg > "/dev/stderr"; exit 1 }
function boiler(l) {
	return (l ~ /^\\restrict / || l ~ /^\\unrestrict / || l ~ /^SET [A-Za-z_.]+ (=|TO) .*;$/ \
		|| l ~ /^SELECT pg_catalog\.set_config\(.*\);$/ || l ~ /^-- Dumped from / || l ~ /^-- Dumped by /)
}
function is_hdr(i) {
	return (lines[i] == "--" && lines[i + 1] ~ /^-- Name: .*; Type: .*; Schema: .*; Owner:/ && lines[i + 2] == "--")
}
function parse_hdr(i,    s, a) {
	s = lines[i + 1]
	sub(/^-- Name: /, "", s)
	a = index(s, "; Type: ")
	hname = substr(s, 1, a - 1)
	s = substr(s, a + 8)
	a = index(s, "; Schema: ")
	htype = substr(s, 1, a - 1)
	s = substr(s, a + 10)
	a = index(s, "; Owner:")
	hschema = substr(s, 1, a - 1)
}
function allowed(t) {
	return (t == "EXTENSION" || t == "COMMENT" || t == "FUNCTION" || t == "TABLE" || t == "SEQUENCE" \
		|| t == "SEQUENCE OWNED BY" || t == "DEFAULT" || t == "CONSTRAINT" || t == "FK CONSTRAINT" \
		|| t == "CHECK CONSTRAINT" || t == "INDEX" || t == "TRIGGER")
}
function is_ext_comment(k) { return (ctype[k] == "COMMENT" && cname[k] ~ /^EXTENSION /) }
# emit_plain: every line except the boilerplate.
function emit_plain(lo, hi,    i) {
	for (i = lo; i <= hi; i++) if (!boiler(lines[i])) print lines[i]
}
# after_close(l): the state once the closing dollar quote (gtag) is read from l.
function after_close(l,    tl) {
	tl = substr(l, index(l, gtag) + length(gtag))
	return (tl ~ /;[ \t]*$/) ? 9 : 3
}
# after_open(l): the state after l, while looking for the opening dollar quote.
function after_open(l,    rest) {
	if (!match(l, /\$[A-Za-z0-9_]*\$/)) return 1
	gtag = substr(l, RSTART, RLENGTH)
	rest = substr(l, RSTART + RLENGTH)
	if (index(rest, gtag) == 0) return 2
	return after_close(rest)
}
# emit_function: a FUNCTION section. The statement (CREATE through the closing
# dollar quote and its semicolon) is copied verbatim and, when generating,
# wrapped so goose does not split the body at semicolons. Lines before and
# after it get the plain treatment. States: 0 before CREATE, 1 looking for the
# opening dollar quote, 2 inside the body, 3 after the body until the
# semicolon, 4 after the statement.
function emit_function(lo, hi,    i, l, st) {
	st = 0
	for (i = lo; i <= hi; i++) {
		l = lines[i]
		if (st == 0) {
			if (l ~ /^CREATE (OR REPLACE )?FUNCTION /) {
				if (mode == "generate") print "-- +goose StatementBegin"
				print l
				st = after_open(l)
			} else if (!boiler(l)) print l
		} else if (st == 1) {
			print l
			st = after_open(l)
		} else if (st == 2) {
			print l
			if (index(l, gtag) > 0) st = after_close(l)
		} else if (st == 3) {
			print l
			if (l ~ /;[ \t]*$/) st = 9
		} else if (!boiler(l)) print l
		if (st == 9) {
			if (mode == "generate") print "-- +goose StatementEnd"
			st = 4
		}
	}
	if (st != 4) fail("function section " cname_cur " has no recognizable end")
}
{ lines[NR] = $0 }
END {
	n = NR
	nch = 0
	for (i = 1; i <= n - 2; i++) if (is_hdr(i)) { nch++; cs[nch] = i }
	if (nch == 0) fail("no pg_dump section headers found")
	for (k = 1; k <= nch; k++) {
		parse_hdr(cs[k])
		cname[k] = hname; ctype[k] = htype; cschema[k] = hschema
		clo[k] = cs[k]
		chi[k] = (k < nch) ? cs[k + 1] - 1 : n
	}
	nt = 0; nf = 0; nfk = 0; nseq = 0; ntrg = 0
	for (k = 1; k <= nch; k++) {
		if (cname[k] ~ /^goose_db_version/) { skip[k] = 1; continue }
		if (mode != "generate") continue
		if (!allowed(ctype[k])) fail("unrecognized pg_dump section type \"" ctype[k] "\" (" cname[k] "); the generator stops rather than guess")
		if (ctype[k] != "FUNCTION") {
			for (i = clo[k]; i <= chi[k]; i++) if (lines[i] ~ /CONCURRENTLY/) fail("concurrent index in " cname[k] ": goose runs the baseline in a transaction")
		}
		if (ctype[k] == "EXTENSION") {
			if (cname[k] != "btree_gist" && cname[k] != "citext") fail("unexpected extension " cname[k])
			found = 0
			for (i = clo[k]; i <= chi[k]; i++) if (lines[i] ~ /^CREATE EXTENSION IF NOT EXISTS (btree_gist|citext) WITH SCHEMA public;$/) { extline[cname[k]] = lines[i]; found = 1 }
			if (!found) fail("extension " cname[k] " is not in the CREATE EXTENSION IF NOT EXISTS ... WITH SCHEMA public form")
			skip[k] = 1
		} else if (is_ext_comment(k)) {
			if (cname[k] != "EXTENSION btree_gist" && cname[k] != "EXTENSION citext") fail("unexpected comment on " cname[k])
			skip[k] = 1
		} else if (ctype[k] == "TABLE") {
			if (cschema[k] != "public") fail("table " cname[k] " is outside schema public")
			tname[++nt] = cname[k]
			haveTable[cname[k]] = 1
		} else if (ctype[k] == "FUNCTION") {
			fname[++nf] = cname[k]
			if (cschema[k] != "public") fail("function " cname[k] " is outside schema public")
		} else if (ctype[k] == "SEQUENCE") {
			sname[++nseq] = cname[k]
		} else if (ctype[k] == "SEQUENCE OWNED BY") {
			seqOwned[cname[k]] = 1
		} else if (ctype[k] == "TRIGGER") {
			ntrg++
		} else if (ctype[k] == "FK CONSTRAINT") {
			from = ""; to = ""
			for (i = clo[k]; i <= chi[k]; i++) {
				if (lines[i] ~ /^ALTER TABLE ONLY public\.[a-z0-9_]+$/) { from = lines[i]; sub(/^ALTER TABLE ONLY public\./, "", from) }
				if (match(lines[i], /REFERENCES public\.[a-z0-9_]+\(/)) { to = substr(lines[i], RSTART, RLENGTH - 1); sub(/^REFERENCES public\./, "", to) }
			}
			if (from == "" || to == "") fail("cannot read the tables of foreign key " cname[k])
			nfk++; fkFrom[nfk] = from; fkTo[nfk] = to
		}
	}
	if (mode == "generate") {
		if (!("citext" in extline) || !("btree_gist" in extline)) fail("the dump lacks the citext or btree_gist extension")
		if (!("users" in haveTable)) fail("the dump has no users table for the Down guard")
		print "-- +goose Up"
		print "-- Baseline for the single-tenant schema before the first church (DEC-138, M7.7)."
		print "-- It replaces the " nfiles "-file chain 00001 through " sprintf("%05d", head) " (no 00021 ever existed; head"
		print "-- version " head "), frozen at commit " frozen_sha ". Generated by"
		print "-- scripts/baseline-proof.sh generate from pg_dump --schema-only of that chain and"
		print "-- proved equal by a second pg_dump diff. Edits before the first tagged release or"
		print "-- first church deployment are made here directly (DEC-138); after that this file"
		print "-- is frozen like any other applied migration and the next change is 00002."
		print extline["citext"]
		print extline["btree_gist"]
		print ""
	}
	emit_plain(1, cs[1] - 1)
	for (k = 1; k <= nch; k++) {
		if (skip[k]) continue
		if (ctype[k] == "FUNCTION") { cname_cur = cname[k]; emit_function(clo[k], chi[k]) }
		else emit_plain(clo[k], chi[k])
	}
	if (mode != "generate") exit 0
	# Down: guard, then drops in reverse dependency order from the object list.
	print ""
	print "-- +goose Down"
	print "-- This Down destroys the whole schema. It exists for scratch databases and the"
	print "-- down/up migration test, and it refuses while any user exists. It is not"
	print "-- application rollback: there is no way back to the pre-squash chain (DEC-138)."
	print "-- +goose StatementBegin"
	print "DO $$ BEGIN"
	print "    IF EXISTS (SELECT 1 FROM users) THEN"
	print "        RAISE EXCEPTION 'baseline down would destroy data; refusing';"
	print "    END IF;"
	print "END $$;"
	print "-- +goose StatementEnd"
	for (i = 1; i <= nfk; i++) if (fkFrom[i] != fkTo[i]) { if (!(fkFrom[i] in haveTable) || !(fkTo[i] in haveTable)) fail("foreign key between unknown tables " fkFrom[i] " and " fkTo[i]); refs[fkTo[i]]++ }
	left = nt
	while (left > 0) {
		progress = 0
		for (i = 1; i <= nt; i++) {
			t = tname[i]
			if (dropped[t] || refs[t] > 0) continue
			print "DROP TABLE public." t ";"
			dropped[t] = 1; left--; progress = 1
			for (j = 1; j <= nfk; j++) if (fkFrom[j] == t && fkTo[j] != t) refs[fkTo[j]]--
		}
		if (!progress) fail("foreign keys form a cycle, so no drop order exists; extend the generator, never hand-write the Down")
	}
	for (i = 1; i <= nseq; i++) if (!(sname[i] in seqOwned)) print "DROP SEQUENCE public." sname[i] ";"
	for (i = nf; i >= 1; i--) print "DROP FUNCTION public." fname[i] ";"
	print "DROP EXTENSION btree_gist;"
	print "DROP EXTENSION citext;"
	printf "baseline-proof: %d tables, %d functions, %d triggers, %d foreign keys\n", nt, nf, ntrg, nfk > "/dev/stderr"
}
AWK

# clean_dump IN OUT: the section 5.2 normalization both sides of the diff share.
clean_dump() { awk -v mode=clean "$DUMP_AWK" "$1" >"$2"; }

# generate_baseline IN OUT: the baseline from the old chain's raw dump.
generate_baseline() {
	awk -v mode=generate -v frozen_sha="$FROZEN_SHA" -v nfiles="$FROZEN_FILES" -v head="$FROZEN_HEAD" \
		"$DUMP_AWK" "$1" >"$2"
	grep -q '^-- +goose Up$' "$2" || die "the generated baseline lacks -- +goose Up"
	if grep -qiE 'goose_db_version|COMMENT ON EXTENSION|CONCURRENTLY' "$2"; then
		die "the generated baseline contains a construct section 5.2 forbids"
	fi
}

# ---------------------------------------------------------------------------
# Stages
# ---------------------------------------------------------------------------
rand_hex() { od -An -N16 -tx1 /dev/urandom | tr -d ' \n'; }

stage_old() {
	say "old chain: migrating $DB_OLD with the frozen tree"
	empty_db "$DB_OLD"
	fold_migrate "$OLD_BIN" "$DB_OLD" up >"$EVIDENCE/old-migrate-up.txt"
	fold_migrate "$OLD_BIN" "$DB_OLD" status >"$EVIDENCE/old-migrate-status.txt"
	local rows
	rows=$(psql_at "$(client_db "$DB_OLD")" "SELECT count(*) FILTER (WHERE version_id > 0), max(version_id), count(*) FILTER (WHERE NOT is_applied) FROM goose_db_version")
	[[ $rows == "$FROZEN_FILES|$FROZEN_HEAD|0" ]] || die "the old chain stored [$rows], want $FROZEN_FILES|$FROZEN_HEAD|0 (files|head|unapplied)"
	psql_at "$(client_db "$DB_OLD")" "SELECT version_id, is_applied FROM goose_db_version ORDER BY id" >"$EVIDENCE/stored-versions-$DB_OLD.txt"
	dump_db "$DB_OLD" "$EVIDENCE/old.raw.sql"
	clean_dump "$EVIDENCE/old.raw.sql" "$EVIDENCE/old.normalized.sql"
}

stage_new() {
	say "candidate: applying the baseline to an empty $DB_NEW"
	empty_db "$DB_NEW"
	[[ $(emptiness "$(client_db "$DB_NEW")") == 0 ]] || die "$DB_NEW is not empty before the candidate is applied"
	fold_migrate "$NEW_BIN" "$DB_NEW" up >"$EVIDENCE/new-migrate-up.txt"
	fold_migrate "$NEW_BIN" "$DB_NEW" status --json >"$EVIDENCE/new-migrate-status.json"
	[[ $(<"$EVIDENCE/new-migrate-status.json") == "$BASELINE_STATUS" ]] ||
		die "migrate status is [$(<"$EVIDENCE/new-migrate-status.json")], want $BASELINE_STATUS"
	require_baseline_rows "$DB_NEW"
	dump_db "$DB_NEW" "$EVIDENCE/new.raw.sql"
	clean_dump "$EVIDENCE/new.raw.sql" "$EVIDENCE/new.normalized.sql"
}

stage_diff() {
	local rc=0 bytes
	diff -u --label old-chain --label candidate "$EVIDENCE/old.normalized.sql" "$EVIDENCE/new.normalized.sql" >"$EVIDENCE/schema.diff" || rc=$?
	((rc <= 1)) || die "diff failed"
	bytes=$(wc -c <"$EVIDENCE/schema.diff")
	printf '%s\n' "$bytes" >"$EVIDENCE/schema.diff.bytes"
	say "normalized schema diff: $bytes bytes"
	if ((bytes != 0)); then
		if ((ALLOW_DIFF)); then
			say "non-empty diff allowed by --allow-diff; check it against the slice's allow-list (R9 section 10)"
		else
			head -n 80 "$EVIDENCE/schema.diff" >&2
			die "the normalized schema diff is not empty (see $EVIDENCE/schema.diff); stop condition, do not hand-edit the baseline"
		fi
	fi
}

# The baseline applied by a non-superuser migration role, with citext and
# btree_gist already created by a different role (section 10 item 1).
drop_owner_fixture() {
	admin_sql "DROP DATABASE IF EXISTS $DB_OWNER WITH (FORCE)"
	admin_sql "DROP ROLE IF EXISTS $ROLE_MIGRATOR"
	admin_sql "DROP ROLE IF EXISTS $ROLE_EXTOWNER"
}

stage_extension_owner() {
	say "extension owner: baseline as a non-superuser with the extensions created by another role"
	local role ownpw migpw expected
	for role in "$ROLE_EXTOWNER" "$ROLE_MIGRATOR"; do
		if [[ $(admin_sql "SELECT count(*) FROM pg_roles WHERE rolname = '$role'") != 0 ]]; then
			((RECREATE)) || die "role $role exists; pass --recreate to drop it (a proof role only)"
		fi
	done
	if db_exists "$DB_OWNER" || [[ $(admin_sql "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'fold_squash_%'") != 0 ]]; then
		((RECREATE)) || die "$DB_OWNER or its roles exist; pass --recreate"
		drop_owner_fixture >/dev/null
	fi
	ownpw=$(rand_hex)
	migpw=$(rand_hex)
	ROLES_CREATED=1
	admin_sql "CREATE ROLE $ROLE_EXTOWNER LOGIN SUPERUSER PASSWORD '$ownpw'" >/dev/null
	admin_sql "CREATE ROLE $ROLE_MIGRATOR LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD '$migpw'" >/dev/null
	admin_sql "CREATE DATABASE $DB_OWNER OWNER $ROLE_MIGRATOR" >/dev/null
	psql_at "$(user_url "$CLIENT_URL" "$ROLE_EXTOWNER" "$ownpw" "$DB_OWNER")" \
		"CREATE EXTENSION citext WITH SCHEMA public; CREATE EXTENSION btree_gist WITH SCHEMA public" >/dev/null
	expected="btree_gist=$ROLE_EXTOWNER,citext=$ROLE_EXTOWNER"
	[[ $(psql_at "$(client_db "$DB_OWNER")" "SELECT string_agg(e.extname || '=' || r.rolname, ',' ORDER BY e.extname) FROM pg_extension e JOIN pg_roles r ON r.oid = e.extowner WHERE e.extname IN ('btree_gist', 'citext')") == "$expected" ]] ||
		die "the extensions are not owned by $ROLE_EXTOWNER before the migration"
	env "${PG_UNSET[@]}" -u APP_ENV -u SESSION_SECRET PGPASSFILE=/dev/null "DATABASE_URL=$(user_url "$SERVER_URL" "$ROLE_MIGRATOR" "$migpw" "$DB_OWNER")" \
		"$NEW_BIN" migrate up >"$EVIDENCE/extension-owner-migrate-up.txt" ||
		die "the baseline failed for a non-superuser migration role with pre-created extensions"
	require_baseline_rows "$DB_OWNER"
	[[ $(psql_at "$(client_db "$DB_OWNER")" "SELECT string_agg(e.extname || '=' || r.rolname, ',' ORDER BY e.extname) FROM pg_extension e JOIN pg_roles r ON r.oid = e.extowner WHERE e.extname IN ('btree_gist', 'citext')") == "$expected" ]] ||
		die "the migration changed an extension owner"
	[[ $(psql_at "$(client_db "$DB_OWNER")" "SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = 'users'") == "$ROLE_MIGRATOR" ]] ||
		die "the users table is not owned by the migration role"
	drop_owner_fixture >/dev/null
	ROLES_CREATED=0
}

boot_listeners() {
	ss -H -ltnp "sport = :$BOOT_PORT"
}

# require_boot_port_free: the fresh-boot proof counts a health answer only from
# its own child, so any listener already on the port stops the run, untouched.
require_boot_port_free() {
	local listeners
	listeners=$(boot_listeners) || die "cannot inspect the fresh-boot port"
	[[ -z $listeners ]] || die "fresh-boot port $BOOT_PORT is already in use; leave its listener untouched and choose another port with --boot-port"
}

boot_owns_listener() {
	local listeners
	kill -0 "$BOOT_PID" 2>/dev/null || return 1
	listeners=$(boot_listeners) || die "cannot inspect the fresh-boot listener"
	[[ -n $listeners ]] || return 1
	# Every listener on this port must belong to our child. Fail closed when ss
	# cannot report ownership; an unrelated listener must never prove our boot.
	local listener
	while IFS= read -r listener; do
		[[ $listener == *"pid=$BOOT_PID,"* ]] || return 1
	done <<<"$listeners"
}

stage_fresh_boot() {
	say "fresh boot: empty database, migrate up, serve, /healthz"
	local boot_tree="$TMP/candidate"
	if [[ $MODE == proof ]]; then boot_tree=$CANDIDATE_TREE; fi
	require_boot_port_free
	empty_db "$DB_BOOT"
	fold_migrate "$NEW_BIN" "$DB_BOOT" up >"$EVIDENCE/boot-migrate-up.txt"
	fold_migrate "$NEW_BIN" "$DB_BOOT" status --json >"$EVIDENCE/boot-migrate-status.json"
	[[ $(<"$EVIDENCE/boot-migrate-status.json") == "$BASELINE_STATUS" ]] ||
		die "fresh-boot migrate status is [$(<"$EVIDENCE/boot-migrate-status.json")]"
	require_baseline_rows "$DB_BOOT"
	# The server runs from a scratch directory so nothing it writes (uploads)
	# lands in the checkout; exec makes $! the server's own pid.
	mkdir -p "$TMP/boot"
	cp -a "$boot_tree/templates" "$TMP/boot/templates"
	(cd "$TMP/boot" && exec env "${PG_UNSET[@]}" -u SESSION_SECRET PGPASSFILE=/dev/null APP_ENV=dev "PORT=$BOOT_PORT" "DATABASE_URL=$(server_db "$DB_BOOT")" \
		"$NEW_BIN" serve) >"$EVIDENCE/boot-server.log" 2>&1 &
	BOOT_PID=$!
	local i ok=0
	for ((i = 0; i < 60; i++)); do
		kill -0 "$BOOT_PID" 2>/dev/null || die "the fresh-boot process exited before proving health (see $EVIDENCE/boot-server.log)"
		if boot_owns_listener &&
			curl --noproxy '*' -fsS --max-time 2 "http://127.0.0.1:$BOOT_PORT/healthz" >"$EVIDENCE/boot-healthz.txt" 2>/dev/null &&
			boot_owns_listener; then
			ok=1
			break
		fi
		sleep 0.5
	done
	((ok)) || die "the fresh-boot process did not own a healthy listener (see $EVIDENCE/boot-server.log)"
	kill "$BOOT_PID" 2>/dev/null || true
	wait "$BOOT_PID" 2>/dev/null || true
	BOOT_PID=""
	require_baseline_rows "$DB_BOOT"
}

# An old-chain database is not refused by the new binary (R9 section 7.7).
stage_old_chain() {
	say "old chain: the new binary does not notice a version-$FROZEN_HEAD database"
	local before after
	empty_db "$DB_CHAIN"
	fold_migrate "$OLD_BIN" "$DB_CHAIN" up >"$EVIDENCE/chain-old-migrate-up.txt"
	before=$(stored_rows "$DB_CHAIN")
	fold_migrate "$NEW_BIN" "$DB_CHAIN" up >"$EVIDENCE/chain-new-migrate-up.txt" ||
		die "the new binary's migrate up failed on an old-chain database; R9 section 7.7 says it succeeds untouched"
	fold_migrate "$NEW_BIN" "$DB_CHAIN" status >"$EVIDENCE/chain-new-migrate-status.txt" || true
	after=$(stored_rows "$DB_CHAIN")
	[[ $before == "$after" ]] || die "the new binary changed the stored versions of an old-chain database"
	[[ $(psql_at "$(client_db "$DB_CHAIN")" "SELECT max(version_id) FROM goose_db_version") == "$FROZEN_HEAD" ]] ||
		die "the old-chain database does not reach version $FROZEN_HEAD"
	printf '%s\n' "$after" >"$EVIDENCE/stored-versions-$DB_CHAIN.txt"
}

write_summary() {
	{
		echo "mode: $MODE"
		echo "frozen commit: $FROZEN_SHA ($FROZEN_FILES files, head $FROZEN_HEAD)"
		echo "checkout HEAD: $(git -C "$REPO" rev-parse HEAD)"
		echo "go: $(go version)"
		echo "psql: $("${PSQL[@]}" --version)"
		echo "pg_dump: $("${PGDUMP[@]}" --version)"
		echo "connection identity:"
		sed 's/^/  /' "$EVIDENCE/connection-identity.txt"
		echo "candidate baseline sha256: $(sha256sum "$CANDIDATE_SQL" | cut -d' ' -f1)"
		echo "schema diff bytes: $(<"$EVIDENCE/schema.diff.bytes")"
		echo "stored versions in $DB_NEW: $(tr '\n' ' ' <"$EVIDENCE/stored-versions-$DB_NEW.txt")"
		echo "git status:"
		git -C "$REPO" status --short
	} >"$EVIDENCE/summary.txt"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
prepare_evidence() {
	mkdir -p "$EVIDENCE"
	[[ -z $(find "$EVIDENCE" -mindepth 1 -print -quit) ]] || die "--evidence-dir must be empty"
	EVIDENCE=$(cd "$EVIDENCE" && pwd)
	frozen_manifest >"$EVIDENCE/frozen-manifest.sha256"
}

install_baseline() {
	say "install: replacing the migration chain with the proven baseline"
	require_frozen_manifest "$REPO" "the working tree"
	local fresh="$REPO/migrations.baseline-new.$$" old="$REPO/migrations.replaced.$$"
	[[ ! -e $fresh && ! -e $old ]] || die "scratch directories already exist next to migrations/"
	cp -a "$REPO/migrations" "$fresh"
	find "$fresh" -maxdepth 1 -type f -name '*.sql' -delete
	install -m 0644 "$CANDIDATE_SQL" "$fresh/00001_baseline.sql"
	mv "$REPO/migrations" "$old"
	mv "$fresh" "$REPO/migrations"
	cmp -s "$CANDIDATE_SQL" "$REPO/migrations/00001_baseline.sql" || die "the installed baseline differs from the proven one"
	if [[ $old == "$REPO"/migrations.replaced.* ]]; then rm -rf -- "$old"; fi
	say "installed migrations/00001_baseline.sql; run: scripts/baseline-proof.sh proof --expect-generated ..."
}

main() {
	parse_args "$@"
	trap cleanup EXIT
	require_tools
	collect_pg_env
	check_manifest_shape
	require_frozen_commit
	TMP=$(mktemp -d "${TMPDIR:-/tmp}/baseline-proof.XXXXXX")

	extract_frozen "$TMP/frozen"
	require_frozen_manifest "$TMP/frozen" "the archive of commit $FROZEN_SHA"
	verify_goose_provider "$TMP/frozen"
	if [[ $MODE == generate ]]; then
		# Regenerating over S3 or S4 edits would erase them.
		require_frozen_manifest "$REPO" "the working tree's migrations (generate only runs on the frozen chain)"
	else
		local sqls
		sqls=$(find "$CANDIDATE_TREE/migrations" -maxdepth 1 -type f -name '*.sql' | wc -l)
		if ((sqls != 1)) || [[ ! -f $CANDIDATE_TREE/migrations/00001_baseline.sql ]]; then
			die "the candidate tree must hold exactly migrations/00001_baseline.sql (found $sqls SQL files)"
		fi
	fi

	# Nothing above touched a database. Before the first read of one, and long
	# before the first change, prove that the Go route and the psql route reach
	# the same cluster, database and role (S2-R1), and that the boot port is free
	# (S2-R2).
	if [[ $MODE == proof ]]; then require_same_driver "$CANDIDATE_TREE"; fi
	build_ident "$TMP/frozen"
	verify_routes
	require_boot_port_free
	scratch_server_check
	prepare_evidence
	printf '%s\n' "$ROUTE_IDENTITY" >"$EVIDENCE/connection-identity.txt"
	say "evidence directory: $EVIDENCE"

	OLD_BIN=$TMP/fold-old
	build_fold "$TMP/frozen" "$OLD_BIN"
	stage_old

	if [[ $MODE == generate ]]; then
		CANDIDATE_SQL=$EVIDENCE/00001_baseline.sql
		generate_baseline "$EVIDENCE/old.raw.sql" "$CANDIDATE_SQL"
		extract_frozen "$TMP/candidate"
		find "$TMP/candidate/migrations" -maxdepth 1 -type f -name '*.sql' -delete
		install -m 0644 "$CANDIDATE_SQL" "$TMP/candidate/migrations/00001_baseline.sql"
		build_fold "$TMP/candidate" "$TMP/fold-new"
	else
		CANDIDATE_SQL=$EVIDENCE/00001_baseline.sql
		cp "$CANDIDATE_TREE/migrations/00001_baseline.sql" "$CANDIDATE_SQL"
		if ((EXPECT_GENERATED)); then
			generate_baseline "$EVIDENCE/old.raw.sql" "$EVIDENCE/regenerated-baseline.sql"
			cmp -s "$EVIDENCE/regenerated-baseline.sql" "$CANDIDATE_SQL" ||
				die "the candidate baseline is not what the generator produces from the frozen chain (compare $EVIDENCE/regenerated-baseline.sql)"
			say "the candidate baseline equals the generator output"
		fi
		build_fold "$CANDIDATE_TREE" "$TMP/fold-new"
	fi
	NEW_BIN=$TMP/fold-new

	stage_new
	stage_diff
	stage_extension_owner
	stage_fresh_boot
	stage_old_chain
	write_summary
	say "all proofs passed"
	if ((INSTALL)); then install_baseline; fi
}

main "$@"
