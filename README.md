# MySQL InnoDB Cluster on Docker

A self-bootstrapping, 3-node **MySQL 8.4 InnoDB Cluster** with **Group Replication** and **MySQL Router**, running on Docker Compose.

One command brings up the nodes, creates the cluster, adds the members and starts the router. It needs no manual `mysqlsh` steps. Running it again is safe: it detects an existing cluster, recovers from a full outage, and re-adds or rejoins missing nodes.

An optional **monitoring stack** (mysqld-exporter, Prometheus and Grafana) shows the cluster live: which node is primary, members going offline and coming back, writes moving to the new primary during a failover, and replication backlog.

> **This is a learning lab.** All three nodes run on one machine, so it isn't real high availability. In production, run the nodes on separate hosts or availability zones.

---

## Architecture

```
                 ┌──────────────────────────┐
   App  ───────► │      MySQL Router        │
                 │ 6446 R/W   │   6447 R/O  │
                 └─────┬──────┴──────┬──────┘
                       │             │
          ┌────────────▼──┐   ┌──────▼────────┐   ┌───────────────┐
          │    mysql1     │◄─►│    mysql2     │◄─►│    mysql3     │
          │   PRIMARY     │   │  SECONDARY    │   │  SECONDARY    │
          └───────────────┘   └───────────────┘   └───────────────┘
                     Group Replication (Paxos-based consensus)
```

| Component | Role |
|---|---|
| **Group Replication** | Keeps 3 servers in sync. If the primary fails, a new one is elected automatically. |
| **MySQL Shell (AdminAPI)** | Creates and manages the cluster (run by `cluster-init`). |
| **MySQL Router** | Sends the app to the current primary (writes) or to the secondaries (reads). |
| **cluster-init** | One-shot container that builds or recovers the cluster, then exits. |
| **monitoring-init** | One-shot container that creates the read-only `exporter` user. |
| **mysqld-exporter** ×3 | One per node. Reads that node's status and Group Replication tables and exposes them as metrics. |
| **Prometheus** | Collects metrics from the 3 exporters every 5 seconds. |
| **Grafana** | Shows the metrics on a ready-made dashboard. |

### Monitoring flow

```
 mysql1 ◄── exporter-mysql1 ──┐
 mysql2 ◄── exporter-mysql2 ──┼──► Prometheus :9090 ──► Grafana :3000
 mysql3 ◄── exporter-mysql3 ──┘      (scrape 5s)        (dashboard)
```

Each exporter connects **directly** to its own node, not through the router. Through the router you would only ever see the primary.

---

## Project structure

```
.
├── docker-compose.yml
├── .env                      # credentials and names (do not commit)
├── conf/
│   ├── common.cnf            # settings shared by all nodes
│   ├── mysql1.cnf            # server_id / report_host per node
│   ├── mysql2.cnf
│   └── mysql3.cnf
├── scripts/
│   ├── init-cluster.sh       # waits for nodes, runs the JS
│   └── init-cluster.js       # idempotent cluster bootstrap
└── monitoring/
    ├── init-exporter-user.sh # creates the read-only exporter user
    ├── prometheus.yml        # scrape config, one target per node
    └── grafana/
        ├── provisioning/
        │   ├── datasources/prometheus.yml   # Prometheus datasource
        │   └── dashboards/dashboards.yml    # where to load dashboards from
        └── dashboards/
            └── mysql-cluster.json           # the dashboard itself
```

---

## Prerequisites

- Docker Desktop or Docker Engine with Compose v2
- About 2 GB of free RAM for the cluster, about 3 GB with monitoring

---

## Quick start

**1. Create `.env`**

```env
MYSQL_ROOT_PASSWORD=rootpass
CLUSTER_ADMIN=clusteradmin
CLUSTER_ADMIN_PASSWORD=adminpass
CLUSTER_NAME=vivekCluster
NETWORK_CLUSTER_NAME=mySqlClusterNetwork

# monitoring
EXPORTER_USER=exporter
EXPORTER_PASSWORD=exporterpass
GRAFANA_ADMIN_PASSWORD=admin
```

You can also copy `.env.example` and fill in the passwords.

**2. Fix file permissions.** MySQL skips config files it can't read, or that anyone can write to, without showing an error. Prometheus and Grafana run as non-root users and need to read their config too.

```bash
chmod 644 conf/*.cnf scripts/* .env
chmod -R a+rX monitoring
```

**3. Start everything**

```bash
docker compose config        # validate the file
docker compose up -d
docker logs -f cluster-init  # watch the cluster being built
```

When `cluster-init` exits with code 0, the router starts on its own. A fresh build takes a minute or two.

**4. Verify**

```bash
# config files were loaded (expect: 2  mysql2)
docker exec mysql2 mysql -uroot -prootpass -e "SELECT @@server_id, @@report_host;"

# router reaches the primary (expect: mysql1)
docker exec mysql2 mysql -h mysql-router -P 6446 -uclusteradmin -padminpass -e "SELECT @@hostname;"

# cluster health
docker exec -it mysql1 mysqlsh --js clusteradmin:adminpass@mysql1:3306 -e "print(dba.getCluster().status())"
```

---

## Connecting your app

| Port | Purpose | Routed to |
|---|---|---|
| `6446` | Read / Write | Current primary |
| `6447` | Read only | Secondaries (load-balanced) |

From the host: `localhost:6446`. From containers on the same network: `mysql-router:6446`.

---

## How the bootstrap works

`cluster-init` runs `scripts/init-cluster.js`, which does the following:

1. **Configures each node** with `dba.configureInstance()` and creates the cluster admin user. Nodes that are already configured are skipped.
2. **Finds the cluster.**
   - Cluster online: uses it.
   - Metadata exists but the group is offline: `dba.rebootClusterFromCompleteOutage()`
   - No metadata: `dba.createCluster()`
3. **Reconciles members.** It adds missing nodes with clone and rejoins nodes that are offline.

| Situation | Result |
|---|---|
| `down -v` then `up -d` | Cluster built from scratch |
| `stop` then `start` | Full outage detected, cluster rebooted |
| `up -d` on a healthy cluster | No changes |
| A node was wiped or removed | Re-added with clone |

---

## Failover test

```bash
# helper: ask the router which node answered
q() { docker run --rm --network mySqlClusterNetwork mysql:8.4 \
      mysql -h mysql-router -P "$1" -uclusteradmin -padminpass -N -e "SELECT @@hostname" 2>/dev/null; }

q 6446   # current primary
q 6447   # a secondary

# kill the primary and measure how long writes are unavailable
P=$(q 6446); echo "killing $P"; docker kill "$P"; s=$(date +%s)
until [ -n "$(q 6446)" ]; do sleep 1; done
echo "new primary $(q 6446) after $(( $(date +%s) - s ))s"

# bring the old primary back: it rejoins as a SECONDARY
docker start "$P"
```

Expect about 10–15 seconds of write downtime. The group first marks the dead node as unreachable (~5s), then waits `group_replication_member_expel_timeout` (5s by default) before electing a new primary.

**After a full outage** (`docker compose stop` / `start`), the cluster is rebuilt from the member with the highest GTID set. The primary may change, so don't assume it's mysql1.

> zsh users: run `setopt interactivecomments` before pasting blocks that contain `#` comments.

**Quorum test:** stop two nodes. Writes block, because a majority (2 of 3) is needed. This is how the cluster prevents split-brain.

---

## Recovery and data consistency

When a killed node is started again, it catches up on everything it missed **before** it serves any traffic. No committed data is lost.

### What happens on `docker start`

1. **Starts read-only.** The node comes up with `super_read_only=ON`, so nothing can write to it while it's out of date.
2. **Rejoins the group.** Its Group Replication settings are persisted in the data volume (`mysqld-auto.cnf`), so it rejoins automatically. State goes `OFFLINE` → `RECOVERING` → `ONLINE`.
3. **Compares GTIDs.** Every transaction has a unique ID (GTID). The node checks which GTIDs it has against the group and requests the missing ones.
4. **Catches up**, in one of two ways:
   - **Incremental recovery** (usual): copies the missing transactions from a donor's binary log and applies them.
   - **Clone**: if the donor no longer has those binary logs, copies a full snapshot of the donor's data instead.
5. **Goes ONLINE** only when fully caught up. Only then does the router send it reads.

New writes made on the primary during recovery are queued and applied afterwards, so nothing in between is missed.

**Why nothing committed is lost:** a transaction is only committed after a majority of the group has agreed on it (certification). Anything the old primary confirmed as committed already existed on at least one other node. Only a transaction that was *in flight* at the moment of the kill is lost. That client got an error, not a commit, so the application knows to retry.

### Test: a secondary falls behind and catches up

```bash
# helper: run SQL through the router (works whichever node is down)
w() { docker run --rm --network mySqlClusterNetwork mysql:8.4 \
      mysql -h mysql-router -P 6446 -uclusteradmin -padminpass -e "$1" 2>/dev/null; }

w "CREATE DATABASE IF NOT EXISTS app; CREATE TABLE IF NOT EXISTS app.t(id INT AUTO_INCREMENT PRIMARY KEY, v INT)"

# 1. kill a SECONDARY (check the primary first with: q 6446)
docker kill mysql3

# 2. write while it is down, so it falls behind
for i in $(seq 1 200); do w "INSERT INTO app.t(v) VALUES ($i)"; done

# 3. bring it back and watch it recover
docker start mysql3
watch -n 2 "docker exec mysql1 mysqlsh --js clusteradmin:adminpass@mysql1:3306 -e 'print(dba.getCluster().status())' 2>/dev/null | grep -E 'address|status\"|memberRole'"
```

`mysql3` shows `RECOVERING`, then `ONLINE`. On macOS without `watch`, rerun the `mysqlsh` command by hand, or install it with `brew install watch`.

With monitoring running, the dashboard shows the same thing:
- **Members ONLINE** stays at 2 while mysql3 is RECOVERING, then returns to 3.
- **GR backlog** briefly shows mysql3's apply queue rising and draining.
- **InnoDB row operations** shows mysql3 inserting the missed rows.

### Test: all nodes hold the same data

Run this once all three nodes are `ONLINE`.

```bash
# 1. same transactions everywhere: all three lines must be identical
for n in mysql1 mysql2 mysql3; do
  echo -n "$n: "; docker exec $n mysql -uroot -prootpass -N -e "SELECT @@GLOBAL.gtid_executed" 2>/dev/null | tr -d '\n'; echo
done

# 2. same data everywhere: same row count and checksum
for n in mysql1 mysql2 mysql3; do
  echo -n "$n: "; docker exec $n mysql -uroot -prootpass -N -e "SELECT COUNT(*) FROM app.t; CHECKSUM TABLE app.t" 2>/dev/null | tr '\n' ' '; echo
done
```

| Check | Proves |
|---|---|
| Identical `gtid_executed` | Every node applied exactly the same set of transactions |
| Identical row count and `CHECKSUM TABLE` | The actual table contents match |

### Test: the killed primary catches up

Repeat the first test, but kill the **primary** (`P=$(q 6446); docker kill "$P"`) while writes run. The writes pause for about 10–15 seconds during the election, then continue on the new primary. After `docker start "$P"`, the old primary rejoins as a SECONDARY, and the consistency checks above match on all three nodes.

---

## Monitoring

### Start

```bash
docker compose --profile monitoring up -d
```

The monitoring services use a Compose **profile**, so a plain `docker compose up -d` still starts only the cluster.

| URL | What |
|---|---|
| http://localhost:3000 | Grafana → **Dashboards → MySQL HA → MySQL InnoDB Cluster** (login `admin` / `GRAFANA_ADMIN_PASSWORD`) |
| http://localhost:9090 | Prometheus → *Status → Target health*: 3 exporters + Prometheus itself, all UP |

Startup order is handled by `depends_on`. `monitoring-init` waits for the router and creates the `exporter` user on the primary; Group Replication copies it to every node. The exporters start only after that has finished successfully.

### Verify

```bash
# each exporter can log in (expect: mysql_up 1, three times)
for n in 1 2 3; do echo -n "exporter-mysql$n: "; docker exec exporter-mysql$n wget -qO- localhost:9104/metrics | grep '^mysql_up'; done

# each exporter watches the right node (expect server_id 1, 2, 3)
for n in 1 2 3; do docker exec exporter-mysql$n wget -qO- localhost:9104/metrics | grep '^mysql_global_variables_server_id'; done

# Group Replication metrics are available
docker exec exporter-mysql1 wget -qO- localhost:9104/metrics | grep -o '^mysql_perf_schema_[a-z_]*' | sort -u
```

### Dashboard

Grafana loads the datasource and the dashboard from files at startup (*provisioning*), so nothing is configured by hand and the dashboard is kept in git.

| Section | Panels |
|---|---|
| Top row | Nodes reachable · Members ONLINE · Current primary · Statements/sec |
| Traffic | Statements/sec by node · CRUD statements by type · Client writes by node |
| Replication | InnoDB row operations · GR backlog (certify and apply queues) · Proposed vs applied transactions · Certification conflicts and rollbacks |
| Resources | Connections · Network traffic |
| Bottom | Group members table (host, role, state, version) |

Hover over the ⓘ icon next to a panel title for an explanation.

| Panel | Metric source |
|---|---|
| Nodes reachable | `mysql_up` |
| Current primary, Members ONLINE, Group members | `mysql_perf_schema_replication_group_member_info` |
| GR backlog | `mysql_perf_schema_transactions_in_queue`, `mysql_perf_schema_transactions_remote_in_applier_queue` |
| Proposed vs applied | `mysql_perf_schema_transactions_local_proposed_total`, `..._remote_applied_total` |
| Conflicts & rollbacks | `mysql_perf_schema_conflicts_detected_total`, `..._transactions_local_rollback_total` |
| Statements, CRUD, writes per node | `mysql_global_status_commands_total` |
| InnoDB rows | `mysql_global_status_innodb_row_ops_total` |

To edit the dashboard: change it in Grafana, then use *Export → Export as JSON* and overwrite `monitoring/grafana/dashboards/mysql-cluster.json`. Changes made only in the UI are lost on `down -v`.

### Useful PromQL

Try these in Prometheus (http://localhost:9090) or Grafana *Explore*:

```promql
mysql_up                                                     # 1 = node reachable
max by (member_host) (mysql_perf_schema_replication_group_member_info{member_role="PRIMARY"})
sum by (node) (rate(mysql_global_status_commands_total{command=~"insert|update|delete"}[1m]))
max by (node) (mysql_perf_schema_transactions_remote_in_applier_queue)
```

`mysql_perf_schema_replication_group_member_info` returns 9 rows, not 3: each of the 3 exporters reports every member. Dashboard queries use `max by (member_host)` to remove the duplicates.

### Watch a failover live

1. Open the dashboard (it refreshes every 5s).
2. Generate some writes:
   ```bash
   for i in $(seq 1 300); do
     docker run --rm --network mySqlClusterNetwork mysql:8.4 mysql -h mysql-router -P 6446 -uclusteradmin -padminpass -e \
       "CREATE DATABASE IF NOT EXISTS app; CREATE TABLE IF NOT EXISTS app.t(id INT AUTO_INCREMENT PRIMARY KEY, v INT); INSERT INTO app.t(v) VALUES ($i);" 2>/dev/null
   done
   ```
3. In another terminal, kill the primary (see *Failover test*).

What you should see:

| Time | Panel |
|---|---|
| immediately | **Nodes reachable** drops to 2 |
| ~10s | **Members ONLINE** drops to 2, **Current primary** changes |
| ~10–15s | **Client writes by node** continues on the new primary |
| after `docker start` | Members back to 3; **GR backlog** may rise briefly while the node catches up |

---

## Restarting after a test

| What happened | Command |
|---|---|
| One node killed | `docker start mysql1` (the node you killed). It rejoins as a SECONDARY in 10–30s. |
| Node running but still `MISSING`/`OFFLINE` | `docker compose up -d cluster-init`. The init script rejoins offline members. |
| Two or three nodes stopped | `docker start mysql1 mysql2 mysql3` then `docker compose up -d cluster-init` (reboots from complete outage) |
| Router or monitoring stopped | `docker compose --profile monitoring up -d` |

Check which nodes are down: `docker compose ps -a mysql1 mysql2 mysql3`.

**Shortcut after any test:** `docker compose --profile monitoring up -d && docker compose up -d cluster-init`.

---

## Day-to-day commands

| Goal | Command |
|---|---|
| Pause, keep data | `docker compose stop` |
| Resume | `docker compose start` (or `up -d`) |
| Wipe and rebuild | `docker compose down -v` then `up -d` |
| Re-run bootstrap | `docker compose up -d cluster-init` |
| Router logs | `docker logs -f mysql-router` |
| Start with monitoring | `docker compose --profile monitoring up -d` |
| Stop everything incl. monitoring | `docker compose --profile monitoring stop` |
| Wipe everything incl. monitoring | `docker compose --profile monitoring down -v` |
| Reload Grafana after editing files | `docker compose --profile monitoring restart grafana` |
| Exporter logs | `docker logs exporter-mysql1` |

> Commands without `--profile monitoring` ignore the monitoring services. For example, `docker compose down` leaves them running and the network stays in use.

AdminAPI commands (inside `mysqlsh`, after `var c = dba.getCluster()`):

| Task | Command |
|---|---|
| Health check | `c.status({extended: 1})` |
| Planned switchover | `c.setPrimaryInstance('mysql2:3306')` |
| Fix metadata issues | `c.rescan()` |
| Rejoin a node | `c.rejoinInstance('mysql3:3306')` |
| Remove a node | `c.removeInstance('mysql3:3306')` |
| Full outage recovery | `dba.rebootClusterFromCompleteOutage()` |

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `@@report_host` is `NULL` | The `.cnf` files weren't read. Run `chmod 644 conf/*.cnf` and restart the nodes. |
| `cluster-init`: `Can't connect ... mysqld.sock` | `mysqlsh` tried a local socket. Start it with `--uri "root:${MYSQL_ROOT_PASSWORD}@mysql1:3306"` in `init-cluster.sh`. |
| `Instance is not managed by InnoDB cluster` | `addInstance` was interrupted by the clone restart. Run `cluster.rescan()`. |
| `Network ... Resource is still in use` | A container outside the current compose config is still attached. Run `docker compose down --remove-orphans`. |
| Router keeps restarting | The cluster isn't ready yet. Check `docker logs cluster-init`. |
| Exporter `Restarting (1)` | A bad command-line argument. Check `docker logs exporter-mysql1`. `command:` must be a YAML list of flags, because the image has no shell to expand variables. |
| `mysql_up 0` | The exporter can't log in. The password variable must be `MYSQLD_EXPORTER_PASSWORD` (with a **D**), and `monitoring-init` must have finished. |
| One node never shows in Grafana, another looks doubled | Two exporters point at the same node. Check each `--mysqld.address` and `mysql_global_variables_server_id`. |
| Grafana folder **MySQL HA** is empty | The dashboard JSON is in the wrong folder. It belongs in `monitoring/grafana/dashboards/`, not in `provisioning/dashboards/`. |
| Prometheus: *"metric might not be a counter"* | Harmless. MySQL's status counters (e.g. `mysql_global_status_queries`) don't end in `_total`. |
| Group Replication panels show "No data" | Check the metric names: search `mysql_perf_schema_` in Prometheus. The exporter needs `--collect.perf_schema.replication_group_members` and `..._member_stats`. |
| `no such service: #` | A trailing `# comment` was passed to Compose. Remove it or run `setopt interactivecomments` in zsh. |

---

## Design notes

- **Config in files, not flags.** `common.cnf` holds the shared settings; each node file holds only its identity. The `01-` and `02-` mount prefixes set the load order.
- **Group Replication settings are not in `.cnf`.** AdminAPI stores them with `SET PERSIST` in `mysqld-auto.cnf` inside each data volume.
- **`restart: unless-stopped` is required.** A clone restarts mysqld. Inside a container that stops the container, and Docker has to bring it back.
- **Single-primary mode.** Multi-primary adds write conflicts and certification failures. Learn single-primary first.
- **One exporter per node, connected directly.** Each member reports its own view, so you can see a node falling behind or dropping out. Going through the router would only ever show the primary.
- **Monitoring as a Compose profile.** It's optional and adds about 1 GB of RAM, so the base cluster stays light.
- **Everything provisioned from files.** The datasource, dashboard provider and dashboard are in git, so a fresh `up -d` produces the same Grafana every time.

## Limitations

- All nodes run on one host. A host failure takes down the whole cluster.
- Router bootstraps through `mysql1`. If `mysql1` is down when the router restarts, bootstrap fails.
- Credentials are kept in `.env`. Use Docker secrets or a vault in production.

## Next steps

- **Benchmark with sysbench**: standard read/write load, then failover under load
- **Load generator app**: CRUD in a loop, measuring write downtime and stale reads during failover
- **InnoDB ClusterSet** for disaster recovery across data centers
- **InnoDB ReplicaSet** for simpler asynchronous setups
- **MySQL Operator for Kubernetes**

---

## License

MIT
