# Lesson 2: Failover and recovery

[← Lesson 1: InnoDB Cluster](01-innodb-cluster.md) · [README](../README.md) · Next: [Lesson 3: Monitoring →](03-monitoring.md)

**Branch:** `01-cluster-setup` (same setup as Lesson 1)

What happens when a node dies, how long writes are unavailable, and how a node that comes back catches up without losing data. All tests use the helpers from [Lesson 0](00-setup.md): run `source scripts/helpers.sh` in every terminal.

---

## Before every test: three members ONLINE

```bash
members          # 3 rows, all ONLINE, one PRIMARY
```

If a node is missing or not `ONLINE` (left over from an earlier test), fix that first: `docker start mysql1 mysql2 mysql3` and `docker compose up -d cluster-init`. With only two active members, killing the primary leaves 1 of 2: no majority, **no new primary**, and the "failover test" silently becomes a [quorum test](#quorum-test).

## Failover test

**Terminal 1**, the transaction loop ([Lesson 0](00-setup.md#transaction-loop)):

```bash
source scripts/helpers.sh
txloop
```

**Terminal 2**, kill the primary and measure until a new one answers:

```bash
source scripts/helpers.sh
P=$(q 6446); echo "killing $P"; docker kill "$P"; s=$(date +%s)
until new=$(q 6446 2>/dev/null) && [ -n "$new" ]; do
  [ $(( $(date +%s) - s )) -ge 90 ] && { echo "no new primary after 90s, run: members mysql2"; break; }
  sleep 1
done
[ -n "$new" ] && echo "new primary $new after $(( $(date +%s) - s ))s"

docker start "$P"        # the old primary rejoins as a SECONDARY
```

Terminal 1 shows a few `FAIL` lines for 15–20 seconds, then `OK` with a **different** `node=`.

### What happens, second by second

From a real run (router log times are UTC):

| Time | Event | Where to see it |
|---|---|---|
| 0s | Primary (mysql3) killed | `docker kill` |
| 0s | Router can't reach the primary: *"Could not connect to the writable cluster member"* | `docker logs mysql-router1` |
| 1s | Router sees **no primary** (both survivors `RO`) and **stops accepting connections on 6446**. Clients get `ERROR 2003 ... Can't connect to remote MySQL server` at once instead of being sent somewhere wrong. | router log, `FAIL` lines |
| ~5s | Survivors mark the dead node *unreachable* | `members mysql2` shows it `UNREACHABLE` |
| ~10s | Group expels it (`group_replication_member_expel_timeout`, default 5s) and elects a new primary | `members mysql2` |
| ~17s | Router sees the new primary (`mode=RW`) and **starts accepting connections** on 6446 again | router log, `OK node=<new>` |

`docker kill` is a hard kill (no clean shutdown), the worst case. A `docker stop` lets the node leave the group cleanly, so failover is almost instant.

### Clients need timeouts

A call that starts in the first second can be handed a connection to the dead primary, which never answers. Without a time limit, that one client waits until HAProxy's idle timeout (1 hour), even though the cluster failed over long ago. That's why the helpers use `--connect-timeout=5` and `timeout 15` ([Lesson 0](00-setup.md#how-a-call-works)): a hung call becomes `FAIL ... timeout after 15s` and the loop continues. Applications need the same: connect and socket timeouts in the connection pool.

Leftover stuck calls from before the timeouts can be removed with `mclean`.

### After a full outage the primary can change

After `docker compose stop` / `start` (or a Docker or Mac restart), `cluster-init` reboots the cluster from the member with the **highest GTID set**, the most up-to-date one. The primary may be mysql2 or mysql3 afterwards. Never assume it's mysql1; ask with `q 6446`.

### Quorum test

Quorum is lost only when members **fail**, not when they leave cleanly:

| How two nodes go away | Result |
|---|---|
| `docker stop` (clean shutdown) | They leave the group politely. The group shrinks to 1 member, which is a majority of 1, so **writes continue**. |
| `docker kill` (crash) | They become *unreachable* but stay in the membership. 1 of 3 is not a majority, so **writes block**. |

```bash
P=$(q 6446); echo "primary: $P"
docker kill $(for n in mysql1 mysql2 mysql3; do [ "$n" != "$P" ] && echo $n; done)
tx 1                     # hangs: the primary can't reach a majority (Ctrl+C to stop)
```

This is how Group Replication prevents split-brain: a minority never accepts writes.

**Recover:** start the killed nodes, then check the status. If it still shows `NO_QUORUM`, force the surviving partition to be the group and rejoin the others:

```bash
docker start mysql1 mysql2 mysql3
docker exec -it "$P" mysqlsh --js "$CLUSTER_ADMIN"@"$P":3306      # asks for the password
```
```js
dba.getCluster().forceQuorumUsingPartitionOf('<primary>:3306')    // e.g. 'mysql2:3306'
```
```bash
docker compose up -d cluster-init      # rejoins the other members
```

---

## How a returning node catches up

When a killed node is started again, it catches up on everything it missed **before** it serves traffic. No committed data is lost.

1. **Starts read-only.** `super_read_only=ON` until it is a healthy member.
2. **Rejoins the group.** Its Group Replication settings are in `mysqld-auto.cnf` in the data volume, so it rejoins automatically. State: `OFFLINE` → `RECOVERING` → `ONLINE`.
3. **Compares GTIDs** with the group and requests the missing transactions.
4. **Catches up**, one of two ways:
   - **Incremental recovery** (usual): copies the missing transactions from a donor's binary log (`log_bin`, `log_replica_updates` in `common.cnf`) and applies them.
   - **Clone**: if the donor no longer has those binary logs, copies a full snapshot instead.
5. **Goes ONLINE** only when caught up. Only then does the router send it reads.

Writes made on the primary during recovery are queued and applied afterwards.

**Why nothing committed is lost:** a transaction commits only after a majority of the group agreed on it (certification). Anything the old primary confirmed already existed on at least one other node. Only a transaction *in flight* at the moment of the kill is lost, and that client got an error, not a commit, so it knows to retry.

---

## Test: a secondary falls behind and catches up

```bash
# 1. kill a SECONDARY (check the primary first with: q 6446)
docker kill mysql3

# 2. write while it is down, so it falls behind
for i in $(seq 1 200); do tx $i; done

# 3. bring it back and watch it recover
docker start mysql3
while true; do clear; members mysql1; sleep 2; done      # Ctrl+C when mysql3 is ONLINE
```

`mysql3` shows `RECOVERING`, then `ONLINE`. With monitoring ([Lesson 3](03-monitoring.md)), the dashboard shows *Members ONLINE* at 2 and then 3, and mysql3's *GR backlog* rising and draining.

## Test: all nodes hold the same data

Once all three are `ONLINE`:

```bash
# 1. same transactions everywhere: all three lines must be identical
for n in mysql1 mysql2 mysql3; do
  echo -n "$n: "; mroot -h $n -P 3306 -N -e "SELECT @@GLOBAL.gtid_executed" | tr -d '\n'; echo
done

# 2. same data everywhere: same row count and checksum
for n in mysql1 mysql2 mysql3; do
  echo -n "$n: "; mroot -h $n -P 3306 -N -e "SELECT COUNT(*) FROM app.t; CHECKSUM TABLE app.t" | tr '\n' ' '; echo
done
```

| Check | Proves |
|---|---|
| Identical `gtid_executed` | Every node applied exactly the same transactions |
| Identical row count and `CHECKSUM TABLE` | The table contents match |

## Test: the killed primary catches up

Repeat the first test, but kill the **primary** (`P=$(q 6446); docker kill "$P"`) while `txloop` runs. Writes pause ~15–20s, then continue on the new primary. After `docker start "$P"` the old primary rejoins as a SECONDARY, and the consistency checks match on all three nodes.

---

## Restarting after a test

| What happened | Command |
|---|---|
| One node killed | `docker start <node>`. Rejoins as a SECONDARY in 10–30s. |
| Node running but still `MISSING`/`OFFLINE` | `docker compose up -d cluster-init` (rejoins offline members) |
| Two or three nodes stopped | `docker start mysql1 mysql2 mysql3`, then `docker compose up -d cluster-init` |
| Docker or the Mac restarted | See [Operations](operations.md#restarting) |

`docker kill` may count as a manual stop, so `restart: unless-stopped` doesn't always bring the node back by itself; start it explicitly.

Next: [Lesson 3: Monitoring →](03-monitoring.md)
