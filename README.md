# MySQL InnoDB Cluster on Docker

A hands-on lab for **MySQL 8.4 high availability**: a self-bootstrapping 3-node InnoDB Cluster with Group Replication, two MySQL Routers behind HAProxy, and a Prometheus + Grafana monitoring stack, all on Docker Compose.

One `docker compose up -d` starts the nodes, creates the cluster, adds the members and starts the routers. Running it again is safe: it detects an existing cluster, recovers from a full outage, and re-adds or rejoins missing nodes.

> **This is a learning lab.** All nodes run on one machine, so it isn't real high availability. In production, run the nodes on separate hosts or availability zones.

```
                        clients
                           │  mysql-router:6446 (R/W)  /  :6447 (R/O)
                           ▼
              ┌──────────────────────────┐
              │  HAProxy  (mysql-router) │  health checks, stats on :8404
              └─────┬──────────────┬─────┘
                    ▼              ▼
             mysql-router1   mysql-router2      MySQL Router 8.4
                    └──────┬───────┘
                           ▼  writes → primary, reads → secondaries
       ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
       │    mysql1    │◄►│    mysql2    │◄►│    mysql3    │
       │   PRIMARY    │  │  SECONDARY   │  │  SECONDARY   │
       └──────────────┘  └──────────────┘  └──────────────┘
              Group Replication (Paxos-based consensus)
                           │
      exporters ──► Prometheus :9090 ──► Grafana :3000
```

---

## Lessons

Each lesson explains its config files setting by setting, then tests the behaviour. Each step also has its own git branch.

| # | Lesson | Branch | You learn |
|---|---|---|---|
| 0 | [Environment and SQL setup](docs/00-setup.md) | | `.env`, `scripts/helpers.sh`, timeouts, database users, transaction loop |
| 1 | [InnoDB Cluster with automatic bootstrap](docs/01-innodb-cluster.md) | `01-cluster-setup` | Group Replication, node config, AdminAPI bootstrap script |
| 2 | [Failover and recovery](docs/02-failover-and-recovery.md) | `01-cluster-setup` | Primary election, write downtime, quorum, catch-up, data consistency |
| 3 | [Monitoring](docs/03-monitoring.md) | `02-monitoring` | mysqld-exporter, Prometheus, Grafana provisioning, dashboard |
| 4 | [Multiple routers behind HAProxy](docs/04-multiple-routers.md) | `03-multi-router` | Router bootstrap, HAProxy health checks, router failure tests |
| 4b | [Router monitoring](docs/04-multiple-routers.md#router-panels) | `04-router-monitoring` | HAProxy metrics in Prometheus, router panels in Grafana |
| 5 | [Read consistency and read/write splitting](docs/05-consistency.md) | `05-consistency` | Stale reads, `group_replication_consistency` levels, router port 6450 |
| 6 | [Failover tuning and switchover](docs/06-failover-tuning.md) | `06-failover-tuning` | `setPrimaryInstance`, `memberWeight`, expel timeout, partitions, auto-rejoin |
| 10 | [Spring Boot application](docs/10-spring-app.md) | `10-spring-app` | Read/write pools, retry, idempotent writes, app-side outage tracking |
| | [Operations and troubleshooting](docs/operations.md) | | Commands, restarting, every known problem and its fix |

---

## Quick start

Requires Docker Desktop or Docker Engine with Compose v2, and about 3 GB of free RAM.

```bash
cp .env.example .env                      # then set the passwords
chmod 644 conf/*.cnf scripts/* .env
chmod -R a+rX monitoring haproxy

docker compose --profile monitoring up -d
docker logs --tail 100 -f cluster-init    # wait for the cluster status JSON
```

Then, in every terminal you test from:

```bash
source scripts/helpers.sh                 # loads .env, creates client.cnf, defines the SQL helpers
mhelp                                     # list of helpers
```

and once, create the app user and test table ([Lesson 0](docs/00-setup.md#create-the-app-user-and-the-test-table)).

| URL | What |
|---|---|
| `localhost:6446` / `localhost:6447` | MySQL read/write / read-only, through HAProxy |
| `localhost:6450` | MySQL read/write splitting, through HAProxy |
| http://localhost:8404/stats | HAProxy: router health and sessions |
| http://localhost:9090 | Prometheus |
| http://localhost:3000 | Grafana → *MySQL HA → MySQL InnoDB Cluster* |

Stop: `docker compose --profile monitoring stop`. Wipe: `docker compose --profile monitoring down -v`. More in [Operations](docs/operations.md).

---

## Project structure

```
.
├── README.md
├── docs/                     # lessons 0–4 and operations
├── docker-compose.yml        # all services
├── .env.example              # template for .env (credentials, names)
├── .env                      # your values (git-ignored)
├── client.cnf                # mysql client logins, generated from .env (git-ignored)
├── .editorconfig             # final newline + LF (HAProxy needs the newline)
├── conf/
│   ├── common.cnf            # settings shared by all nodes          → Lesson 1
│   └── mysql1..3.cnf         # server_id / report_host per node      → Lesson 1
├── scripts/
│   ├── init-cluster.sh       # waits for nodes, runs the JS          → Lesson 1
│   ├── init-cluster.js       # idempotent AdminAPI bootstrap         → Lesson 1
│   └── helpers.sh            # SQL test helpers: source it per shell → Lesson 0
├── haproxy/
│   └── haproxy.cfg           # load balancer for the two routers     → Lesson 4
└── monitoring/
    ├── init-exporter-user.sh # creates the read-only exporter user   → Lesson 3
    ├── prometheus.yml        # scrape config                         → Lesson 3
    └── grafana/
        ├── provisioning/     # datasource + dashboard provider       → Lesson 3
        └── dashboards/       # mysql-cluster.json                    → Lesson 3
```

---

## License

MIT
