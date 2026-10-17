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
  echo "$CLUSTER_ADMIN_PASSWORD" | docker exec -i "${1:-mysql1}" \
    mysqlsh --js --passwords-from-stdin "$CLUSTER_ADMIN@${1:-mysql1}:3306" -e "print(dba.getCluster().status())"
}

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
mclean           kill stuck helper containers                 mclean
Timeout per SQL call: MH_TIMEOUT (default 15s)
HELP
}

echo "helpers: loaded (user=$APP_USER, network=$NETWORK_CLUSTER_NAME, timeout=${MH_TIMEOUT}s). Run 'mhelp' for the list."
