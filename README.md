# MySQL InnoDB Cluster on Docker

A self-bootstrapping, 3-node **MySQL 8.4 InnoDB Cluster** with **Group Replication** and **MySQL Router**, running on Docker Compose.

One command brings up the nodes, creates the cluster, adds the members and starts the router. It needs no manual `mysqlsh` steps. Running it again is safe: it detects an existing cluster, recovers from a full outage, and re-adds or rejoins missing nodes.

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
└── scripts/
    ├── init-cluster.sh       # waits for nodes, runs the JS
    └── init-cluster.js       # idempotent cluster bootstrap
```

---

## Prerequisites

- Docker Desktop or Docker Engine with Compose v2
- About 2 GB of free RAM

---

## Quick start

**1. Create `.env`**

```env
MYSQL_ROOT_PASSWORD=rootpass
CLUSTER_ADMIN=clusteradmin
CLUSTER_ADMIN_PASSWORD=adminpass
CLUSTER_NAME=vivekCluster
NETWORK_CLUSTER_NAME=mySqlClusterNetwork
```

**2. Fix file permissions.** MySQL skips config files it can't read, or that anyone can write to, without showing an error.

```bash
chmod 644 conf/*.cnf scripts/* .env
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
# write some data
docker exec mysql2 mysql -h mysql-router -P 6446 -uclusteradmin -padminpass -e \
  "CREATE DATABASE IF NOT EXISTS app;
   CREATE TABLE IF NOT EXISTS app.t(id INT PRIMARY KEY, v VARCHAR(20));
   INSERT INTO app.t VALUES (1,'before');"

# simulate a crash of the primary
docker kill mysql1

# a new primary is elected within seconds; the same port still works
docker exec mysql2 mysql -h mysql-router -P 6446 -uclusteradmin -padminpass -e \
  "SELECT @@hostname; INSERT INTO app.t VALUES (2,'after');"
```

Because of `restart: unless-stopped`, Docker restarts `mysql1` and it rejoins as a **secondary**. Use `docker stop mysql1` instead to keep it down.

**Quorum test:** stop two nodes. Writes block, because a majority (2 of 3) is needed. This is how the cluster prevents split-brain.

---

## Day-to-day commands

| Goal | Command |
|---|---|
| Pause, keep data | `docker compose stop` |
| Resume | `docker compose start` (or `up -d`) |
| Wipe and rebuild | `docker compose down -v` then `up -d` |
| Re-run bootstrap | `docker compose up -d cluster-init` |
| Router logs | `docker logs -f mysql-router` |

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

---

## Design notes

- **Config in files, not flags.** `common.cnf` holds the shared settings; each node file holds only its identity. The `01-` and `02-` mount prefixes set the load order.
- **Group Replication settings are not in `.cnf`.** AdminAPI stores them with `SET PERSIST` in `mysqld-auto.cnf` inside each data volume.
- **`restart: unless-stopped` is required.** A clone restarts mysqld. Inside a container that stops the container, and Docker has to bring it back.
- **Single-primary mode.** Multi-primary adds write conflicts and certification failures. Learn single-primary first.

## Limitations

- All nodes run on one host. A host failure takes down the whole cluster.
- Router bootstraps through `mysql1`. If `mysql1` is down when the router restarts, bootstrap fails.
- Credentials are kept in `.env`. Use Docker secrets or a vault in production.

## Next steps

- **InnoDB ClusterSet** for disaster recovery across data centers
- **InnoDB ReplicaSet** for simpler asynchronous setups
- **MySQL Operator for Kubernetes**

---

## License

MIT
