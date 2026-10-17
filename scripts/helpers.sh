# SQL test helpers for the mysql-ha lab.
#
# Load in every new terminal (bash or zsh), from anywhere:
#   source scripts/helpers.sh
#
# It loads .env, (re)generates client.cnf when .env changed, and defines the
# helper functions below. No password is ever put on a command line.

# ---- locate the project folder (works in bash and zsh) -----------------------
if [ -n "${ZSH_VERSION:-}" ]; then
  _mh_src=$(eval 'echo ${(%):-%x}')
  setopt interactivecomments 2>/dev/null   # allow pasting blocks with # comments
else
  _mh_src="${BASH_SOURCE[0]}"
fi
MH_DIR="$(cd "$(dirname "$_mh_src")/.." && pwd)"
unset _mh_src

if [ ! -f "$MH_DIR/.env" ]; then
  echo "helpers: $MH_DIR/.env not found (cp .env.example .env and set the passwords)" >&2
  return 1 2>/dev/null || exit 1
fi

# ---- .env -> shell variables -------------------------------------------------
set -a; . "$MH_DIR/.env"; set +a

# ---- client.cnf (git-ignored), regenerated when .env is newer ----------------
if [ ! -f "$MH_DIR/client.cnf" ] || [ "$MH_DIR/.env" -nt "$MH_DIR/client.cnf" ]; then
  printf '[client]\nuser=%s\npassword=%s\nhost=mysql-router\nport=6446\n\n[client_root]\nuser=root\npassword=%s\n' \
    "$APP_USER" "$APP_PASSWORD" "$MYSQL_ROOT_PASSWORD" > "$MH_DIR/client.cnf"
  chmod 644 "$MH_DIR/client.cnf"
  echo "helpers: client.cnf generated from .env"
fi

# Max seconds per SQL call. Without a limit, a call that starts during a
# failover can wait on a dead connection for up to HAProxy's 1h idle timeout.
MH_TIMEOUT="${MH_TIMEOUT:-15}"

# ---- core: a throwaway mysql client on the cluster network -------------------
# Each call runs in its own container, labelled so stuck ones can be cleaned up.
_mh_mysql() {
  docker run --rm --label mysql-ha.client=1 --network "$NETWORK_CLUSTER_NAME" \
    -v "$MH_DIR/client.cnf:/client.cnf:ro" mysql:8.4 \
    timeout "$MH_TIMEOUT" mysql --defaults-extra-file=/client.cnf "$@"
}

# mysql as the app user, through the load balancer (mysql-router:6446)
m() { _mh_mysql --connect-timeout=5 "$@"; }

# mysql as root; add -h mysqlN -P 3306 to talk to one node directly
mroot() { _mh_mysql --defaults-group-suffix=_root --connect-timeout=5 "$@"; }

# which node answers on a port: q 6446 (primary) / q 6447 (a secondary)
q() { m -P "${1:-6446}" -N -e "SELECT @@hostname"; }

# run SQL on the primary as the app user
w() { m -e "$1"; }

# one test transaction: OK with the committing node, or FAIL with the error
tx() {
  local out node id
  out=$(m -N -B -e "INSERT INTO app.t(v) VALUES ($1); SELECT @@hostname, LAST_INSERT_ID();" 2>&1)
  if [ $? -eq 0 ]; then
    read -r node id <<< "$out"
    printf '%s  OK    #%-6s node=%-7s id=%s\n' "$(date +%T)" "$1" "$node" "$id"
  else
    [ -z "$out" ] && out="ERROR timeout after ${MH_TIMEOUT}s"
    printf '%s  FAIL  #%-6s %s\n' "$(date +%T)" "$1" "$(echo "$out" | grep -m1 ERROR)"
  fi
}

# endless transaction loop, appended to tx.log (Ctrl+C to stop)
txloop() {
  local i=0
  while true; do i=$((i+1)); tx "$i"; done | tee -a "$MH_DIR/tx.log"
}

# Group Replication membership as seen by one node (default mysql1)
members() {
  mroot -h "${1:-mysql1}" -P 3306 -e \
    "SELECT MEMBER_HOST, MEMBER_STATE, MEMBER_ROLE FROM performance_schema.replication_group_members"
}

# full cluster status from MySQL Shell (default mysql1)
mstatus() {
  echo "$CLUSTER_ADMIN_PASSWORD" | docker exec -i -e LC_ALL=C.UTF-8 "${1:-mysql1}" \
    mysqlsh --js --passwords-from-stdin "$CLUSTER_ADMIN@${1:-mysql1}:3306" -e "print(dba.getCluster().status())"
}

# set primary to specific node (default mysql1)
mprimary() {
  local target_node="${1:-mysql1}"
  local connect_node="${2:-mysql2}"

  echo "Promoting $target_node to primary..."
  
  echo "$CLUSTER_ADMIN_PASSWORD" | docker exec -i -e LC_ALL=C.UTF-8 "$connect_node" \
    mysqlsh --js --passwords-from-stdin "$CLUSTER_ADMIN@$connect_node:3306" \
    -e "dba.getCluster().setPrimaryInstance('$target_node:3306'); print(dba.getCluster().status())"
}

# first mysqlN container that is running (used when no node is given)
_mh_up_node() {
  local n
  for n in mysql1 mysql2 mysql3; do
    [ "$(docker inspect -f '{{.State.Running}}' "$n" 2>/dev/null)" = true ] && { echo "$n"; return; }
  done
  echo mysql1
}

# run AdminAPI JavaScript with `cluster` already defined (default: first running node)
# usage: mjs "<js>" [node]    e.g.  mjs "cluster.setOption('expelTimeout', 0)"
mjs() {
  local js="${1:?usage: mjs \"<js>\" [node]}" node="${2:-$(_mh_up_node)}"
  echo "$CLUSTER_ADMIN_PASSWORD" | docker exec -i -e LC_ALL=C.UTF-8 "$node" \
    mysqlsh --js --passwords-from-stdin "$CLUSTER_ADMIN@$node:3306" \
    -e "var cluster = dba.getCluster(); $js"
}

# failover-related Group Replication settings, per node
mgrvars() {
  local n out a b c d e fmt='%-7s %-6s %-6s %-15s %-6s %s\n'
  printf "$fmt" node expel rejoin exit_action weight unreachable_majority
  for n in mysql1 mysql2 mysql3; do
    out=$(MH_TIMEOUT=5 mroot -h "$n" -P 3306 -N -B -e "SELECT
      @@group_replication_member_expel_timeout, @@group_replication_autorejoin_tries,
      @@group_replication_exit_state_action, @@group_replication_member_weight,
      @@group_replication_unreachable_majority_timeout" 2>/dev/null)
    if [ -n "$out" ]; then
      read -r a b c d e <<< "$out"; printf "$fmt" "$n" "$a" "$b" "$c" "$d" "$e"
    else
      printf "$fmt" "$n" - - unreachable - -
    fi
  done
}

# failover timeline from all logs, sorted (times are UTC, docker log time)
# usage: mevents [since]     e.g.  mevents 3m   or   mevents 2026-10-17T12:15:45Z
mevents() {
  local since="${1:-3m}" c
  local pat='unreachable|members removed|was elected|working as primary|expelled|auto-rejoin|rejoin|offline_mode|majority|routing:bootstrap_rw listening|\[(WARNING|ALERT)\].*routers_rw'
  for c in mysql1 mysql2 mysql3 mysql-router1 mysql-router2 mysql-router; do
    docker logs -t --since "$since" "$c" 2>&1 | grep -iE "$pat" | awk -v c="$c" '{
      t = substr($1, 12, 12)
      sub(/^[^ ]+ /, ""); sub(/.*reported: /, ""); sub(/.*INFO \[[0-9a-f]+\] /, ""); sub(/.*\) : /, "")
      printf "%s  %-13s %s\n", t, c, substr($0, 1, 110)
    }'
  done | sort
}

# SQL as root *inside* a node's container: works even when the node is cut off
# from the network (mroot cannot reach it then). usage: mnode <node> "SQL"
mnode() {
  local node="${1:?usage: mnode <node> \"SQL\"}" sql="${2:?usage: mnode <node> \"SQL\"}"
  docker exec -i "$node" sh -c 'cat > /tmp/mh.cnf; timeout "$1" mysql --defaults-extra-file=/tmp/mh.cnf \
    --defaults-group-suffix=_root -h 127.0.0.1 -P 3306 -e "$2"; rc=$?; rm -f /tmp/mh.cnf; exit $rc' \
    _ "$MH_TIMEOUT" "$sql" < "$MH_DIR/client.cnf"
}

# network partition: cut a node off the cluster network, and plug it back in
mcut()  { docker network disconnect "$NETWORK_CLUSTER_NAME" "${1:?usage: mcut <node>}" && echo "$(date +%T) $1 cut off"; }
mjoin() { docker network connect --alias "${1:?usage: mjoin <node>}" "$NETWORK_CLUSTER_NAME" "$1" && echo "$(date +%T) $1 reconnected"; }

# Make a SECONDARY fall behind: hold a read lock on app.t so its replication applier waits.
# usage: mstall <node> [seconds]      e.g.  mstall mysql3 30 &
mstall() {
  local node="${1:?usage: mstall <node> [seconds]}" secs="${2:-20}"
  if [ "$(q 6446 2>/dev/null)" = "$node" ]; then
    echo "mstall: $node is the PRIMARY; locking it blocks client writes, not replication. Use a secondary." >&2
    return 1
  fi
  echo "$(date +%T) $node: app.t locked for ${secs}s, its applier falls behind"
  MH_TIMEOUT=$(( secs + 10 )) mroot -h "$node" -P 3306 \
    -e "LOCK TABLES app.t READ; SELECT SLEEP($secs); UNLOCK TABLES" >/dev/null
  echo "$(date +%T) $node: released, catching up"
}

# run SQL through read/write splitting (router port 6450, via HAProxy): prints the result only
# usage: r "SQL"      e.g.  r "SELECT @@hostname"
r() { m -P 6450 -N -e "${1:?usage: r \"SQL\"}"; }

# kill helper containers that are still running (stuck calls)
mclean() {
  local n
  n=$(docker ps -q --filter label=mysql-ha.client=1 | wc -l | tr -d ' ')
  [ "$n" -gt 0 ] && docker kill $(docker ps -q --filter label=mysql-ha.client=1) >/dev/null
  echo "killed $n helper container(s)"
}

mhelp() {
  cat <<'HELP'
m ...            mysql as app user via mysql-router:6446     m -e "SELECT 1"
mroot ...        mysql as root (add -h mysqlN -P 3306)        mroot -h mysql2 -P 3306 -e "..."
q [port]         node answering on 6446 (primary) / 6447      q 6447
w "SQL"          run SQL on the primary as app user           w "SELECT COUNT(*) FROM app.t"
tx N             one test insert, prints OK/FAIL + node       tx 1
txloop           endless tx loop, appended to tx.log          txloop
members [node]   group membership seen by a node              members mysql2
mstatus [node]   cluster status from MySQL Shell              mstatus mysql2
mprimary [to] [via] switch primary to <to> (default mysql1)   mprimary mysql2
mjs "<js>" [node]  AdminAPI JS, `cluster` predefined        mjs "print(cluster.options())"
mgrvars          failover settings on every node              mgrvars
mevents [since]  failover timeline from all logs (UTC)       mevents 3m
mnode <node> "SQL" root SQL inside the node container         mnode mysql1 "SELECT 1"
mcut / mjoin <node> network partition: cut off / reconnect     mcut mysql3
r "SQL"          SQL via read/write splitting (6450)          r "SELECT @@hostname"
mstall <node> [s] lock app.t on a secondary so it lags        mstall mysql3 30 &
mclean           kill stuck helper containers                 mclean
Timeout per SQL call: MH_TIMEOUT (default 15s)
HELP
}

echo "helpers: loaded (user=$APP_USER, network=$NETWORK_CLUSTER_NAME, timeout=${MH_TIMEOUT}s). Run 'mhelp' for the list."
