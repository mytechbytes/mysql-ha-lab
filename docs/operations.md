# Operations and troubleshooting

[← Lesson 4: Multiple routers](04-multiple-routers.md) · [README](../README.md)

Day-to-day commands, recovering after tests or restarts, and fixes for every problem hit while building this lab. Commands using `m`, `mroot`, `members`, `mstatus` need `source scripts/helpers.sh` first.

---

## Start, stop, remove

Monitoring services use a Compose profile; add `--profile monitoring` or commands skip them.

| Goal | Command | Data kept? |
|---|---|---|
| Start everything | `docker compose --profile monitoring up -d` | |
| Start cluster only | `docker compose up -d` | |
| Pause | `docker compose --profile monitoring stop` | ✅ containers and volumes |
| Resume | `docker compose --profile monitoring up -d` | |
| Remove containers | `docker compose --profile monitoring down` | ✅ volumes stay |
| Remove everything | `docker compose --profile monitoring down -v --remove-orphans` | ❌ volumes deleted; cluster rebuilt next time |
| Re-run the bootstrap | `docker compose up -d cluster-init` | |

After `down -v`, redo [Lesson 0](00-setup.md#create-the-app-user-and-the-test-table): create the app user and table again.

| Flag | Meaning |
|---|---|
| `--profile monitoring` | Include exporters, Prometheus, Grafana, monitoring-init |
| `-v` | Also delete named volumes (`mysql1..3`, `prometheus_data`, `grafana_data`) |
| `--remove-orphans` | Remove containers of services no longer in the compose file |

## Logs and status

| What | Command |
|---|---|
| Bootstrap | `docker logs --tail 100 -f cluster-init` |
| A node | `docker logs --tail 100 -f mysql1` |
| Routers | `docker logs --tail 100 -f mysql-router1` (or `mysql-router2`) |
| HAProxy | `docker logs --tail 100 -f mysql-router` |
| monitoring-init | `docker logs --tail 100 -f monitoring-init` |
| An exporter | `docker logs --tail 100 -f exporter-mysql1` |
| Container states | `docker compose --profile monitoring ps -a` |
| HAProxy stats | http://localhost:8404/stats |
| Cluster status | `mstatus` (or `mstatus mysql2` if mysql1 is down) |
| Group members (SQL) | `members` (or `members mysql2`) |
| Stuck helper calls | `docker ps --filter label=mysql-ha.client`; remove with `mclean` |

## Reload after editing a config

| File | Command |
|---|---|
| `conf/*.cnf` | `docker restart mysqlN`, **one node at a time**, checking the status in between to keep quorum |
| `haproxy/haproxy.cfg` | Validate ([Lesson 4](04-multiple-routers.md#validate-the-config)), then `docker compose restart router-lb` |
| `monitoring/prometheus.yml` | `docker compose --profile monitoring restart prometheus` |
| Grafana provisioning or dashboard JSON | `docker compose --profile monitoring restart grafana` |
| `docker-compose.yml` | `docker compose --profile monitoring up -d` (recreates changed services) |

---

## Restarting

| What happened | Command |
|---|---|
| One node killed | `docker start <node>`. Rejoins as SECONDARY in 10–30s. |
| Node running but `MISSING`/`OFFLINE` | `docker compose up -d cluster-init` |
| Two or three nodes stopped | `docker start mysql1 mysql2 mysql3`, then `docker compose up -d cluster-init` |
| Quorum lost (two nodes killed) | See [Lesson 2: Quorum test](02-failover-and-recovery.md#quorum-test) |
| A router killed | `docker start mysql-router1` (HAProxy marks it UP after ~4s) |
| HAProxy stopped | `docker compose up -d router-lb` |
| **Docker or the Mac restarted** | `docker compose --profile monitoring up -d`, then `docker restart mysql-router1 mysql-router2` |

**Why the Docker-restart case needs `up -d`:** all nodes restart at the same moment, so there's no running group to rejoin and the cluster stays offline. `cluster-init` reboots it, but it only runs on `docker compose up`, not when Docker restarts containers by itself. Routers started while there was no primary need a restart to reload the topology.

---

## Troubleshooting

### Cluster

| Symptom | Cause / fix |
|---|---|
| `@@report_host` is `NULL` | The `.cnf` files weren't read. `chmod 644 conf/*.cnf`, restart the nodes. |
| `cluster-init`: `Can't connect ... mysqld.sock` | `mysqlsh` tried a local socket. `init-cluster.sh` must pass `--uri "root:...@mysql1:3306"`. |
| `Instance is not managed by InnoDB cluster` | `addInstance` was interrupted by the clone restart. Run `cluster.rescan()`. |
| *Errant GTIDs* warning on a fresh build | Expected; clone overwrites the image's first-start transactions ([Lesson 1](01-innodb-cluster.md#errant-gtids-warning-on-a-fresh-build)). |
| Writes hang, status `NO_QUORUM` | A majority is unreachable. [Lesson 2: Quorum test](02-failover-and-recovery.md#quorum-test). |
| Primary isn't mysql1 after a restart | Normal: the member with the highest GTID set becomes the seed. |

### Routers and HAProxy

| Symptom | Cause / fix |
|---|---|
| Router keeps restarting | Cluster not ready. `docker logs --tail 100 cluster-init`. |
| `container name "/mysql-router" is already in use` | The old single-router container still exists. `docker rm -f mysql-router`, then `up -d --remove-orphans`. |
| HAProxy exits: `Missing LF on last line` | `haproxy.cfg` lacks its final newline. |
| HAProxy can't read its config | `chmod -R a+rX haproxy` (it runs as non-root) |
| `Unknown MySQL server host 'mysql-router'` | HAProxy isn't running. `docker logs --tail 100 mysql-router`. |
| `Lost connection ... reading initial communication packet` on 6446 | HAProxy is up but no router is healthy, or no primary. Stats page, then group members. |
| Both routers red after a Docker restart | No primary when they started. `docker compose up -d cluster-init`, `docker restart mysql-router1 mysql-router2`. |
| Stale router in `listRouters()` | Container without `hostname:`. `removeRouterMetadata('<name>')`. |
| `haproxy -c`: `[NOTICE] could not resolve address` | Not an error: ran outside the network, or a router isn't running. Check the exit code. |

### Monitoring

| Symptom | Cause / fix |
|---|---|
| `monitoring-init` stuck on "Waiting for router..." | [Lesson 3: debugging steps](03-monitoring.md#if-monitoring-init-is-stuck-on-waiting-for-router). If the whole chain works (`mroot -e "SELECT 1"` succeeds) but it keeps waiting, an attempt is hanging: `docker restart monitoring-init`. |
| Exporter `Restarting (1)` | Bad argument. `command:` must be a YAML list; no shell expands `$VARS` in the container. |
| `mysql_up 0` | Exporter can't log in. Variable must be `MYSQLD_EXPORTER_PASSWORD` (with **D**), and `monitoring-init` must have finished. |
| One node missing in Grafana, another doubled | Two exporters point at the same node. Check `--mysqld.address` and `mysql_global_variables_server_id`. |
| Grafana folder **MySQL HA** empty | Dashboard JSON in the wrong folder: it belongs in `monitoring/grafana/dashboards/`, not `provisioning/dashboards/`. |
| *"metric might not be a counter"* | Harmless; MySQL counters don't end in `_total`. |
| Group Replication panels "No data" | Search `mysql_perf_schema_` in Prometheus; the two `--collect.perf_schema.replication_group_*` flags are required. |

### SQL and users

| Symptom | Cause / fix |
|---|---|
| `ERROR 1142 ... INSERT command denied to user 'clusteradmin'` | By design: `clusteradmin` has no data privileges. Use the `app` user. |
| `ERROR 1146 ... Table 'app.t' doesn't exist` | Create it ([Lesson 0](00-setup.md#create-the-app-user-and-the-test-table)). |
| Writes rejected on a table | Group Replication requires a primary key on every table. |
| *"Using a password on the command line interface can be insecure"* | Use `client.cnf` (`m`/`mroot`) or `--passwords-from-stdin` for `mysqlsh`. |
| `m: command not found`, or an empty user / `--network` error | New terminal: `source scripts/helpers.sh`. |
| A test call hangs; `txloop` stops printing | A connection to a killed node never answers. The helpers' `timeout` ends it after 15s; for calls started before that, `mclean`. Check that `type m` shows `timeout`. |
| Failover test never gets a new primary | Only two members were ONLINE before the kill, so quorum was lost. Run `members` before every test; see [Lesson 2](02-failover-and-recovery.md#before-every-test-three-members-online). |
| `Access denied` after changing `.env` | `source scripts/helpers.sh` regenerates `client.cnf`. For the app user, rerun the `CREATE/ALTER USER` from Lesson 0. Root's password only changes with the volumes (`down -v`). |

### Shell

| Symptom | Fix |
|---|---|
| `no such service: #` / `zsh: number expected` | zsh treats `#` as text. `setopt interactivecomments` (`helpers.sh` sets it). |
| `What's next: Debug this … with Gordon` | Docker CLI hint. `export DOCKER_CLI_HINTS=false`. |

---

## Design notes

- **Config in files, not flags.** `common.cnf` has the shared settings, each node file only its identity; `01-`/`02-` prefixes set load order.
- **Group Replication settings live in `mysqld-auto.cnf`**, written by AdminAPI with `SET PERSIST`, not in the `.cnf` files.
- **`restart: unless-stopped` is required** for clone, which restarts `mysqld`.
- **Idempotent bootstrap.** `cluster-init` creates, reboots, adds or rejoins depending on what it finds, so every `up -d` converges to a healthy cluster.
- **Single-primary mode.** Multi-primary adds write conflicts and certification failures.
- **One exporter per node, connected directly**, so each member reports its own view.
- **HAProxy keeps the name `mysql-router`**, so nothing that connected to the single router had to change.
- **TCP health checks.** Quick detection of a dead router; primary selection stays with MySQL Router.
- **Least-privilege users.** `clusteradmin` manages, `app` owns the data, `exporter` only reads.
- **No passwords on command lines.** `.env` → `scripts/helpers.sh` → `client.cnf` / stdin.
- **Every test call has a timeout**, like a real connection pool, so a dead connection can't freeze a test.
- **Monitoring as a Compose profile**, so the base cluster stays light.
- **Everything provisioned from files**, so a fresh `up -d` gives the same Grafana every time.

## Limitations

- All nodes run on one host; a host failure takes down everything.
- Routers bootstrap through `mysql1`; if it's down when a router restarts, that bootstrap fails.
- HAProxy is a single instance, so a single point of failure.
- The `app` user and table are created by hand.
- Credentials live in `.env`; use Docker secrets or a vault in production.

## Next steps

- Read/write splitting through the router's port 6450
- Benchmark with sysbench: standard load, then failover under load
- A load-generator application measuring write downtime and stale reads
- InnoDB ClusterSet for disaster recovery across data centers
- InnoDB ReplicaSet for simpler asynchronous setups
- MySQL Operator for Kubernetes
