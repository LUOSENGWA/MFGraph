#!/usr/bin/env bash
# Acceptance of MFGraph M1.0a as a module of the host platform (AgentTeams),
# after the host's template (appendix A of its confirmation), adapted to the
# MFGraph API. Run it from the bundle root; it is scripts/acceptance-m1.0a.sh
# in the bundle and packaging/host-module/acceptance-m1.0a.sh in the
# repository.
#
#   scripts/acceptance-m1.0a.sh
#
# Every variable below can be set from the environment. The script talks to
# the module directly over its socket and therefore sets the identity
# headers itself (behind the host, the kernel injects them). It needs bash,
# curl with --unix-socket, python3 and a mysql client (MYSQL) for the
# database checks. It prints the filled difference table (appendix B) and
# exits 1 when a row failed.
set -uo pipefail

# 0) Variables: the names of the template.
BIN=${BIN:-./bin/mfgraph-server}
SOCK=${SOCK:-/run/agentteams/modules/mfgraph.sock}
BASE=${BASE:-http://module/internal/v1/mfgraph}
DATADIR=${DATADIR:-/data/mfgraph}
DSN=${DSN:-'root:@tcp(127.0.0.1:2881)/mfgraph?parseTime=true'}
OPS_DSN=${OPS_DSN:-'root:@tcp(127.0.0.1:2881)/ops?parseTime=true'}
TOKEN=${TOKEN:-dev-token}
# Additions: the fixed tenant, the project of the requests and another one,
# the actor, the header prefix, the configuration file, the mysql client.
TENANT=${TENANT:-01M7K943SH6K5W7N4F1K3J000G}
PROJECT=${PROJECT:-01M7K943SH6K5W7N4F1K3J000G}
OTHER_PROJECT=${OTHER_PROJECT:-01M7K943SH6K5W7N4F1K3J001G}
ACTOR=${ACTOR:-acceptance-user}
ACTOR_TYPE=${ACTOR_TYPE:-human}
HDR=${HDR:-X-Atf-}
CONFIG=${CONFIG:-etc/module.env}
MYSQL=${MYSQL:-mysql -h127.0.0.1 -P2881 -uroot}
EVENTS_DOC=${EVENTS_DOC:-docs/events.md}
MIGRATIONS_DIR=${MIGRATIONS_DIR:-migrations}
# PREPARE_DB=1 creates the two schemas and the host's outbox table when
# they are missing (local runs and MFGraph's CI; the host has them).
PREPARE_DB=${PREPARE_DB:-0}
WORK=${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/mfgraph-acceptance.XXXXXX")}

ORIGIN=$(printf %s "$BASE" | sed -E 's#^(https?://[^/]+).*#\1#')
DB=$(printf %s "$DSN" | sed -E 's#^.*\)/([^?]*).*$#\1#')
OPS_DB=$(printf %s "$OPS_DSN" | sed -E 's#^.*\)/([^?]*).*$#\1#')
# The host's outbox table: in the schema of OPS_DSN (ops.module_outbox, as
# in etc/module.env, with the template's OPS_DSN); passed to the module as
# MFGRAPH_OUTBOX_TABLE.
OUTBOX_TABLE=${OUTBOX_TABLE:-$OPS_DB.module_outbox}
PID=
FAILED=0
mkdir -p "$WORK"
: >"$WORK/results.tsv"

log() { printf '== %s\n' "$*"; }

# result NUMBER PASS|FAIL|HOST MEASURED [NOTE]
result() {
	printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "${4:-}" >>"$WORK/results.tsv"
	[ "$2" = FAIL ] && FAILED=1
	printf '   [%s] #%s %s\n' "$2" "$1" "$3"
}

cleanup() {
	if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
		kill -TERM "$PID" 2>/dev/null
		wait "$PID" 2>/dev/null
	fi
}
trap cleanup EXIT

sql() { $MYSQL -N -B -r "$1" -e "$2"; }

# call METHOD PATH [curl arguments...]: identity headers of PROJECT; sets
# CODE, the body is in $WORK/body.
call() {
	local method=$1 path=$2
	shift 2
	CODE=$(curl -sS --unix-socket "$SOCK" -o "$WORK/body" -w '%{http_code}' -X "$method" \
		-H "${HDR}Internal-Token: $TOKEN" -H "${HDR}Actor: $ACTOR" -H "${HDR}Actor-Type: $ACTOR_TYPE" \
		-H "${HDR}Project: $PROJECT" -H "${HDR}Request-Id: acceptance-$$-$RANDOM" "$@" "$BASE$path")
}

# raw METHOD URL [curl arguments...]: no headers but the given ones.
raw() {
	local method=$1 url=$2
	shift 2
	CODE=$(curl -sS --unix-socket "$SOCK" -o "$WORK/body" -w '%{http_code}' -X "$method" "$@" "$url")
}

field() { python3 -c 'import json,sys
v = json.load(open(sys.argv[1]))
for k in sys.argv[2].split("."):
    v = v[k]
print(v)' "$WORK/body" "$1" 2>/dev/null; }

sha() { python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$1"; }

now() { python3 -c 'import time; print(time.time())'; }

run_migrate() {
	MODULE_ID=mfgraph MFGRAPH_DSN="$DSN" MFGRAPH_KERNEL_MIGRATIONS=false "$BIN" --config "$CONFIG" migrate >"$WORK/migrate-$1.log" 2>&1
}

start_server() {
	MODULE_ID=mfgraph MODULE_SOCKET="$SOCK" MODULE_INTERNAL_TOKEN="$TOKEN" MFGRAPH_DSN="$DSN" MFGRAPH_OPS_DSN="$OPS_DSN" \
		MFGRAPH_DATA_DIR="$DATADIR" MFGRAPH_TENANT_ID="$TENANT" MFGRAPH_KERNEL_MIGRATIONS=false \
		MFGRAPH_OUTBOX_TABLE="$OUTBOX_TABLE" "$BIN" serve --config "$CONFIG" --socket "$SOCK" >>"$WORK/server.log" 2>&1 &
	PID=$!
	for _ in $(seq 1 60); do
		if curl -sf --unix-socket "$SOCK" -o "$WORK/readyz" "$ORIGIN/readyz" 2>/dev/null; then
			return 0
		fi
		kill -0 "$PID" 2>/dev/null || return 1
		sleep 1
	done
	return 1
}

for tool in curl python3; do
	command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 2; }
done
[ -x "$BIN" ] || { echo "no executable $BIN (run from the bundle root)" >&2; exit 2; }
[ -f "$CONFIG" ] || { echo "no $CONFIG (run from the bundle root)" >&2; exit 2; }
log "work directory $WORK; database $DB, host outbox $OUTBOX_TABLE"

if [ "$PREPARE_DB" = 1 ]; then
	log "prepare: schemas $DB and ${OUTBOX_TABLE%%.*}, table $OUTBOX_TABLE"
	$MYSQL -e "CREATE DATABASE IF NOT EXISTS \`$DB\` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin; CREATE DATABASE IF NOT EXISTS \`${OUTBOX_TABLE%%.*}\`" || exit 2
	sql "${OUTBOX_TABLE%%.*}" "CREATE TABLE IF NOT EXISTS ${OUTBOX_TABLE#*.} (
  id BIGINT AUTO_INCREMENT NOT NULL PRIMARY KEY,
  event_id VARCHAR(64) NOT NULL,
  module VARCHAR(64) NOT NULL,
  event_kind VARCHAR(64) NOT NULL,
  scope_kind VARCHAR(16) NOT NULL,
  scope_id VARCHAR(191) NOT NULL,
  actor_type VARCHAR(16) NULL,
  actor_id VARCHAR(255) NULL,
  payload_json JSON NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  attempts INT NOT NULL DEFAULT 0,
  available_at DATETIME(6) NOT NULL,
  published_at DATETIME(6) NULL,
  last_error VARCHAR(1024) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  UNIQUE KEY uk_module_outbox_event (event_id),
  KEY idx_module_outbox_pending (status, available_at),
  KEY idx_module_outbox_scope (scope_kind, scope_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4" || exit 2
	mkdir -p "$DATADIR" "$(dirname "$SOCK")"
	chmod 700 "$(dirname "$SOCK")"
fi

# 1a) Migrations (row 6): twice; versions recorded; no kernel tables; the
# recorded checksums are those of the files in migrations/.
log "migrate (twice)"
if run_migrate 1 && run_migrate 2; then
	files=$(ls "$MIGRATIONS_DIR"/*.sql 2>/dev/null | wc -l | tr -d ' ')
	second=$(grep -c '"component":"mfgraph".*"applied":null.*"skipped":'"$files" "$WORK/migrate-2.log")
	kernel=$(grep -c '"component":"kernel"' "$WORK/migrate-1.log" "$WORK/migrate-2.log" | awk -F: '{s+=$2} END {print s}')
	recorded=$(sql "$DB" "SELECT version, checksum FROM schema_migrations WHERE component = 'mfgraph' ORDER BY version")
	others=$(sql "$DB" "SELECT COUNT(*) FROM schema_migrations WHERE component <> 'mfgraph'")
	ktables=$(sql "$DB" "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '$DB' AND table_name IN ('projects', 'principals')")
	want=$(for f in "$MIGRATIONS_DIR"/*.sql; do printf '%s\t%s\n' "$(basename "$f" .sql)" "$(sha "$f")"; done)
	if [ "$second" = 1 ] && [ "$kernel" = 0 ] && [ "$others" = 0 ] && [ "$ktables" = 0 ] && [ "$recorded" = "$want" ]; then
		result 6 PASS "migrate x2 ok; $files versions recorded (component mfgraph, checksums = migrations/); no kernel tables; --config read (module mode)"
	else
		result 6 FAIL "second run skipped all: $second; kernel runs: $kernel; other components: $others; kernel tables: $ktables; checksums equal: $([ "$recorded" = "$want" ] && echo yes || echo no)" "see $WORK/migrate-*.log"
	fi
else
	result 6 FAIL "migrate failed" "see $WORK/migrate-*.log"
fi

# 1) Start + readyz (row 1), over a stale socket left behind.
log "start"
if [ ! -e "$SOCK" ]; then
	python3 -c 'import socket,sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' "$SOCK" 2>/dev/null
fi
if start_server; then
	ready=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["status"], d["checks"].get("outbox"))' "$WORK/readyz")
	mode=$(stat -c %a "$SOCK" 2>/dev/null || stat -f %Lp "$SOCK")
	tcp="not measured"
	if command -v lsof >/dev/null; then
		tcp=$(lsof -nP -a -p "$PID" -iTCP -sTCP:LISTEN 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')
	elif [ -d "/proc/$PID/fd" ]; then
		# Linux without lsof: the listening TCP sockets among the process's
		# file descriptors.
		tcp=$(python3 -c 'import os, sys
pid = sys.argv[1]
inodes = set()
for fd in os.listdir("/proc/%s/fd" % pid):
    try:
        link = os.readlink("/proc/%s/fd/%s" % (pid, fd))
    except OSError:
        continue
    if link.startswith("socket:["):
        inodes.add(link[8:-1])
n = 0
for table in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        rows = open(table).read().splitlines()[1:]
    except OSError:
        continue
    n += sum(1 for r in rows if r.split()[3] == "0A" and r.split()[9] in inodes)
print(n)' "$PID")
	fi
	listening=$(grep -c "\"msg\":\"listening\",\"addr\":\"$SOCK\"" "$WORK/server.log")
	if [ "$ready" = "ready ok" ] && [ "$mode" = 600 ] && [ "$listening" = 1 ] && { [ "$tcp" = 0 ] || [ "$tcp" = "not measured" ]; }; then
		result 1 PASS "readyz=ready (outbox ok); UDS $mode; TCP listeners: $tcp; listens only on $SOCK"
	else
		result 1 FAIL "readyz: $ready; mode $mode; TCP listeners: $tcp; listening on the socket: $listening"
	fi
else
	result 1 FAIL "the server did not become ready" "see $WORK/server.log"
	tail -n 20 "$WORK/server.log" >&2
	[ -f "$WORK/readyz" ] && cat "$WORK/readyz" >&2
	curl -sS --unix-socket "$SOCK" "$ORIGIN/readyz" >&2
	exit 1
fi

# 2) Walk through: workspace -> upload (upload path) -> commit -> tree ->
# download and compare; then a second commit and a restore.
log "walk through"
call POST /workspaces -H 'Content-Type: application/json' -d "{\"project_id\":\"$PROJECT\",\"display_name\":\"m1a\"}"
WS=$(field workspace_id)
MAIN=$(field main_branch_id)
printf 'hello' >"$WORK/f.txt"
call PUT "/objects/$WS" -H 'Content-Type: application/octet-stream' --data-binary @"$WORK/f.txt"
up=$CODE
HASH=$(field hash)
call POST "/workspaces/$WS/commits" -H 'Content-Type: application/json' \
	-d "{\"branch_id\":\"$MAIN\",\"expected_generation\":1,\"message\":\"m1a\",\"changes\":[{\"path\":\"f.txt\",\"hash\":\"$HASH\"}]}"
c1=$CODE
S1=$(field snapshot_id)
call GET "/snapshots/$S1/tree"
tree=$(python3 -c 'import json,sys; print(",".join(i["path"] for i in json.load(open(sys.argv[1]))["items"]))' "$WORK/body" 2>/dev/null)
call GET "/snapshots/$S1/file?path=f.txt"
dl=$CODE
cp "$WORK/body" "$WORK/g.txt"
same=no
[ "$dl" = 200 ] && [ "$(sha "$WORK/f.txt")" = "$(sha "$WORK/g.txt")" ] && same=yes
printf 'second' >"$WORK/b.txt"
call PUT "/objects/$WS" -H 'Content-Type: application/octet-stream' --data-binary @"$WORK/b.txt"
H2=$(field hash)
call POST "/workspaces/$WS/commits" -H 'Content-Type: application/json' \
	-d "{\"branch_id\":\"$MAIN\",\"expected_generation\":2,\"message\":\"second\",\"changes\":[{\"path\":\"docs/b.txt\",\"hash\":\"$H2\"}]}"
c2=$CODE
S2=$(field snapshot_id)
call POST "/snapshots/$S1/restore" -H 'Content-Type: application/json' -d "{\"branch_id\":\"$MAIN\",\"expected_generation\":3}"
rs=$CODE
S3=$(field snapshot_id)
call GET "/snapshots/$S1"
author="$(field author_ref)/$(field author_type)"
log "workspace $WS: snapshots $S1 $S2 $S3"

# 2b) Prefix (row 2) and identity (row 3).
raw GET "$ORIGIN/api/v1/workspaces/$WS" -H "${HDR}Internal-Token: $TOKEN" -H "${HDR}Actor: $ACTOR" \
	-H "${HDR}Actor-Type: $ACTOR_TYPE" -H "${HDR}Project: $PROJECT"
apiv1=$CODE
if [ "$up" = 201 ] && [ "$c1" = 201 ] && [ "$tree" = f.txt ] && [ "$same" = yes ] && [ "$c2" = 201 ] && [ "$rs" = 201 ] &&
	[ "$apiv1" = 404 ]; then
	result 2 PASS "/internal/v1/mfgraph: workspace, upload (/objects/), commit, tree, download (sha256 equal), restore; /api/v1 404"
else
	result 2 FAIL "upload $up, commit $c1, tree [$tree], download equal: $same, commit $c2, restore $rs, /api/v1 $apiv1"
fi
codes=""
raw GET "$BASE/workspaces/$WS"
codes="$codes no-token:$CODE"
raw GET "$BASE/workspaces/$WS" -H "${HDR}Internal-Token: wrong-$TOKEN" -H "${HDR}Actor: $ACTOR" -H "${HDR}Actor-Type: $ACTOR_TYPE" \
	-H "${HDR}Project: $PROJECT"
codes="$codes wrong-token:$CODE"
for missing in Actor Actor-Type Project; do
	set -- -H "${HDR}Internal-Token: $TOKEN"
	[ "$missing" = Actor ] || set -- "$@" -H "${HDR}Actor: $ACTOR"
	[ "$missing" = Actor-Type ] || set -- "$@" -H "${HDR}Actor-Type: $ACTOR_TYPE"
	[ "$missing" = Project ] || set -- "$@" -H "${HDR}Project: $PROJECT"
	raw GET "$BASE/workspaces/$WS" "$@"
	codes="$codes no-$missing:$CODE"
done
raw GET "$BASE/workspaces/$WS" -H "${HDR}Internal-Token: $TOKEN" -H "${HDR}Actor: $(printf 'a\377b')" -H "${HDR}Actor-Type: $ACTOR_TYPE" \
	-H "${HDR}Project: $PROJECT"
codes="$codes actor-not-utf8:$CODE"
if [ "$codes" = " no-token:401 wrong-token:401 no-Actor:401 no-Actor-Type:401 no-Project:401 actor-not-utf8:401" ] &&
	[ "$author" = "$ACTOR/$ACTOR_TYPE" ]; then
	result 3 PASS "401:$codes; the snapshot's author is the header's actor ($author)"
else
	result 3 FAIL "$codes; author $author"
fi

# 4) ACL (row 4): resources of another project, and /projects.
codes=""
for path in "/workspaces/$WS" "/workspaces/$WS/branches" "/snapshots/$S1" "/snapshots/$S1/tree" "/snapshots/$S1/file?path=f.txt"; do
	raw GET "$BASE$path" -H "${HDR}Internal-Token: $TOKEN" -H "${HDR}Actor: $ACTOR" -H "${HDR}Actor-Type: $ACTOR_TYPE" \
		-H "${HDR}Project: $OTHER_PROJECT"
	codes="$codes $CODE"
done
raw PUT "$BASE/objects/$WS" -H "${HDR}Internal-Token: $TOKEN" -H "${HDR}Actor: $ACTOR" -H "${HDR}Actor-Type: $ACTOR_TYPE" \
	-H "${HDR}Project: $OTHER_PROJECT" -H 'Content-Type: application/octet-stream' --data-binary @"$WORK/f.txt"
codes="$codes $CODE"
call GET "/projects/$PROJECT"
projects="$CODE"
call POST /projects -H 'Content-Type: application/json' -d '{"name":"p"}'
projects="$projects $CODE"
if [ "$codes" = " 404 404 404 404 404 404" ] && [ "$projects" = "404 404" ]; then
	result 4 PASS "another project: 404 for workspace, branches, snapshot, tree, file, upload; /projects 404"
else
	result 4 FAIL "another project:$codes; /projects: $projects"
fi

# 5) Upload limits (row 5): the host's proxy enforces them.
result 5 HOST "the upload path PUT /internal/v1/mfgraph/objects/{workspace_id} works (row 2)" \
	"limits 1 GiB / 600 s on /objects/ and /uploads/, 8 MiB elsewhere: measured by the host"

# 3) SIGTERM (row 8): exit within 8 s, socket removed, data kept.
log "SIGTERM"
t0=$(now)
kill -TERM "$PID"
wait "$PID"
status=$?
secs=$(python3 -c 'import sys; print("%.1f" % (float(sys.argv[2]) - float(sys.argv[1])))' "$t0" "$(now)")
PID=
gone=no
[ -e "$SOCK" ] || gone=yes
if start_server; then
	call GET "/snapshots/$S1/tree"
	again=$(python3 -c 'import json,sys; print(",".join(i["path"] for i in json.load(open(sys.argv[1]))["items"]))' "$WORK/body" 2>/dev/null)
	call GET "/snapshots/$S3/file?path=f.txt"
	cp "$WORK/body" "$WORK/h.txt"
	kept=no
	[ "$CODE" = 200 ] && [ "$(sha "$WORK/f.txt")" = "$(sha "$WORK/h.txt")" ] && kept=yes
else
	again="(no restart)"
	kept=no
fi
if [ "$status" = 0 ] && python3 -c 'import sys; sys.exit(0 if float(sys.argv[1]) <= 8 else 1)' "$secs" && [ "$gone" = yes ] &&
	[ "$again" = f.txt ] && [ "$kept" = yes ]; then
	result 8 PASS "exit $status after ${secs}s; socket removed; after the restart the tree and the restored file are there"
else
	result 8 FAIL "exit $status after ${secs}s; socket removed: $gone; tree after restart [$again]; restored file kept: $kept"
fi

# 4) Outbox (row 7): the events of the workspace in the host table.
log "outbox"
cat >"$WORK/check_events.py" <<'PY'
import hashlib, json, re, sys
rows_file, events_doc, ws, project, actor, actor_type, s1, s2, s3 = sys.argv[1:10]
registered = re.findall(r"^\| `(mfgraph\.[a-z_]+)` \|", open(events_doc, encoding="utf-8").read(), re.M)
rows = []
for line in open(rows_file, encoding="utf-8"):
    if not line.strip():
        continue
    eid, module, kind, scope_kind, scope_id, atype, aid, status, attempts, payload = line.rstrip("\n").split("\t")
    env = json.loads(payload)
    if env.get("data", {}).get("workspace_id") == ws:
        rows.append((eid, module, kind, scope_kind, scope_id, atype, aid, status, attempts, env))
problems = []
want = ["mfgraph.workspace_created", "mfgraph.branch_created", "mfgraph.commit_created", "mfgraph.branch_updated",
        "mfgraph.commit_created", "mfgraph.branch_updated", "mfgraph.snapshot_restored", "mfgraph.branch_updated"]
kinds = [r[2] for r in rows]
if kinds != want:
    problems.append("kinds in order: %s" % kinds)
for eid, module, kind, scope_kind, scope_id, atype, aid, status, attempts, env in rows:
    d = env["data"]
    key, version = {
        "mfgraph.workspace_created": (d.get("workspace_id"), 1),
        "mfgraph.branch_created": (d.get("branch_id"), d.get("generation")),
        "mfgraph.branch_updated": (d.get("branch_id"), d.get("generation")),
    }.get(kind, (d.get("snapshot_id"), 1))
    derived = hashlib.sha256(("mfgraph\n%s\n%s\n%s\n%s" % (kind, project, key, version)).encode()).hexdigest()
    checks = {
        "module": module == "mfgraph", "scope": scope_kind == "project" and scope_id == project,
        "actor columns": atype == actor_type and aid == actor, "status": status == "pending" and attempts == "0",
        "v": env.get("v") == 1, "event_id": env.get("event_id") == eid == derived,
        "occurred_at": re.match(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}Z$", env.get("occurred_at", "")) is not None,
        "project_id": env.get("project_id") == project,
        "actor": env.get("actor") == {"type": actor_type, "id": actor},
        "registered": kind in registered,
    }
    bad = [k for k, ok in checks.items() if not ok]
    if bad:
        problems.append("%s: %s" % (kind, ", ".join(bad)))
reasons = [r[9]["data"].get("reason") for r in rows if r[2] == "mfgraph.branch_updated"]
if reasons != ["commit", "commit", "restore"]:
    problems.append("reasons %s" % reasons)
heads = [r[9]["data"].get("snapshot_id") for r in rows if r[2] in ("mfgraph.commit_created", "mfgraph.snapshot_restored")]
if heads != [s1, s2, s3]:
    problems.append("snapshots %s" % heads)
emitted = sorted(set(kinds))
expected = sorted(set(registered) - {"mfgraph.snapshot_created"})
if emitted != expected or len(registered) != 6:
    problems.append("emitted %s, registered %s" % (emitted, registered))
counts = {k: kinds.count(k) for k in registered}
summary = ", ".join("%s %d" % (k.split(".")[1], counts[k]) for k in registered)
print(("OK " if not problems else "FAIL ") + summary + ("" if not problems else " | " + "; ".join(problems)))
PY
outbox=""
for _ in $(seq 1 20); do
	sql "${OUTBOX_TABLE%%.*}" "SELECT event_id, module, event_kind, scope_kind, scope_id, IFNULL(actor_type, ''), IFNULL(actor_id, ''),
		status, attempts, payload_json FROM ${OUTBOX_TABLE#*.} WHERE module = 'mfgraph' AND scope_id = '$PROJECT' ORDER BY id" >"$WORK/outbox.tsv" || break
	outbox=$(python3 "$WORK/check_events.py" "$WORK/outbox.tsv" "$EVENTS_DOC" "$WS" "$PROJECT" "$ACTOR" "$ACTOR_TYPE" "$S1" "$S2" "$S3")
	case $outbox in OK*) break ;; esac
	sleep 0.5
done
case $outbox in
OK*)
	result 7 PASS "${outbox#OK }; envelope v/event_id/occurred_at/project_id/actor complete; event_id = documented derivation; kinds in docs/events.md" \
		"six kinds registered, five emitted: snapshot_created 0 because M1.0a has no manual snapshot and no snapshot before a restore"
	;;
*) result 7 FAIL "${outbox:-no rows read}" "see $WORK/outbox.tsv" ;;
esac

# Row 9: the binary runs without a host (standalone mode is MFGraph's
# default; its acceptance tests run in MFGraph's CI).
if "$BIN" version >"$WORK/version" 2>&1; then
	result 9 PASS "$(cat "$WORK/version") runs without the host; standalone acceptance tests: MFGraph CI (MySQL 8.0, seekdb)"
else
	result 9 FAIL "$BIN version failed"
fi

# The difference table (appendix B).
python3 - "$WORK/results.tsv" >"$WORK/acceptance-m1.0a-result.md" <<'PY'
import sys
items = {
    "1": ("启动/readyz", "readyz=ready；UDS 0600；无 TCP"),
    "2": ("前缀", "`/internal/v1/mfgraph` 可达；`/api/v1` 404"),
    "3": ("身份", "无 token/错 token 拒绝；claims 头可取 actor"),
    "4": ("ACL", "跨 project 资源 404"),
    "5": ("上传限额", "新路由 1GiB/600s 生效；旧路由 8MiB"),
    "6": ("迁移", "`migrate` 幂等；版本记录；--config 生效"),
    "7": ("事件", "M1.0a 六类事件；envelope 字段齐；event_id 稳定"),
    "8": ("退出", "SIGTERM ≤8s；重启数据在"),
    "9": ("独立形态", "`mfgraph-server` 无宿主可跑"),
}
rows = {}
for line in open(sys.argv[1], encoding="utf-8"):
    n, status, measured, note = line.rstrip("\n").split("\t")
    rows[n] = (status, measured, note)
print("| # | 项 | 期望（契约） | 实测 | 偏差/处理 |")
print("|---|---|---|---|---|")
for n in sorted(items, key=int):
    status, measured, note = rows.get(n, ("FAIL", "not run", ""))
    print("| %s | %s | %s | %s: %s | %s |" % (n, items[n][0], items[n][1], status, measured.replace("|", "/"), note.replace("|", "/") or "—"))
PY
echo
cat "$WORK/acceptance-m1.0a-result.md"
echo
echo "result: $WORK/acceptance-m1.0a-result.md; logs: $WORK"
exit $FAILED
