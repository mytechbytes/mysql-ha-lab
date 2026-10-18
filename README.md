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

Each lesson explains its config files setting by setting, then tests the behaviour. Each step also has its own git branch. New here? Read [How to use this repository](#1-how-to-use-this-repository) first.

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
| 10b | [Go application](apps/labapp-go/) | `step-10b-go-app` | The same app in Go: `database/sql` pools, retry, the same metrics, compared in Grafana |
| | [Operations and troubleshooting](docs/operations.md) | | Commands, restarting, every known problem and its fix |

---

## 1. How to use this repository

Each lesson is a page in [`docs/`](docs/) plus a git branch (and a `step-…` tag) holding the repository as it was at the end of that lesson. `main` has every lesson merged.

1. **Start on `main`** and follow the lessons in order. Each one explains its files and ends with tests you run yourself.
2. **Use one terminal per role.** Most tests use two: one runs `txloop` or an app's load loop, the other breaks something (`docker kill`, `mcut`, `mprimary`).
3. **Run `source scripts/helpers.sh` in every new terminal.** It loads `.env`, writes `client.cnf` and defines the SQL helpers (`mhelp` lists them). Credentials never go on the command line.
4. **Look at a past state** with `git switch 05-consistency` (or any branch from the [Lessons](#lessons) table). Go back with `git switch main`. The `step-…` tags (`git tag`) mark the same points.
5. **Reset before each lesson** (all three members `ONLINE`, see [§3](#3-how-to-test-everything)). After a failed test, use [§4](#4-how-to-clean-and-restart-everything).

Requirements: Docker Desktop or Docker Engine with Compose v2, about 3 GB of free RAM (4 GB with both apps), `jq` for the `curl … | jq` examples. Building and running the apps outside Docker also needs Java 25 + Maven (Spring Boot) or Go 1.22+ (Go).

---

## 2. How to run all the containers and profiles

### First time

```bash
cp .env.example .env                      # then set every password and user
chmod 644 conf/*.cnf scripts/* .env
chmod -R a+rX monitoring haproxy
```

### Profiles

Services without a profile always start. The others start only when their profile is named, **on every command** (`up`, `ps`, `stop`, `down`, `logs`).

| Profile | Adds | Ports |
|---|---|---|
| *(none)* | `mysql1..3`, `cluster-init`, `mysql-router1/2`, HAProxy (`mysql-router`) | 6446 RW · 6447 RO · 6450 RW-split · 8404 HAProxy stats |
| `monitoring` | `monitoring-init`, `exporter-mysql1..3`, `prometheus`, `grafana` | 9090 Prometheus · 3000 Grafana |
| `app-springboot` | `labapp-springboot` | 8080 |
| `app-go` | `labapp-go` | 8090 |
| `app` | both apps | 8080 · 8090 |

### Start

```bash
# everything: cluster + routers + monitoring + both apps
docker compose --profile monitoring --profile app up -d --build
docker logs --tail 100 -f cluster-init    # wait for the cluster status JSON, then Ctrl-C

# or only what a lesson needs
docker compose up -d                                  # Lessons 1–2, 4–6: cluster + routers
docker compose --profile monitoring up -d             # Lesson 3 onward: + Prometheus, Grafana
docker compose --profile monitoring --profile app-springboot up -d --build   # Lesson 10
docker compose --profile monitoring --profile app-go up -d --build           # Lesson 10b
```

`cluster-init` creates the cluster, the `app`, `labapp_springboot` and `labapp_go` users and schemas, and the `app.t` test table. It exits when it is done (state `Exited (0)` is correct). Running `up -d` again is safe.

`--build` is needed the first time and after you change app code. Without it, Compose reuses the existing image.

### Apps in Docker (on the cluster network)

The apps run as containers next to the routers and connect to `mysql-router:6446/6447` inside the Docker network. Prometheus scrapes them as `mode=docker`.

```bash
# Spring Boot -> http://localhost:8080
docker compose --profile app-springboot up -d --build labapp-springboot
docker compose logs --tail 100 -f labapp-springboot

# Go -> http://localhost:8090
docker compose --profile app-go up -d --build labapp-go
docker compose logs --tail 100 -f labapp-go

# both at once
docker compose --profile app up -d --build
```

| Goal | Command |
|---|---|
| Rebuild after a code change | `docker compose --profile app-go up -d --build labapp-go` (or `--profile app-springboot … labapp-springboot`) |
| Restart without rebuilding | `docker compose --profile app restart labapp-go` |
| Start with the load loop running | `LOAD_ENABLED` is `"false"` in `docker-compose.yml`; instead run `curl -s -X POST localhost:8090/load/start` |
| Stop / remove an app | `docker compose --profile app stop labapp-go` / `docker compose --profile app rm -sf labapp-go` |
| Check the DB user it uses | `docker compose --profile app config labapp-go \| grep DB_USER` |

Credentials come from `.env` (`LABAPP_SPRINGBOOT_DB_*`, `LABAPP_GO_DB_*`) through `docker-compose.yml`. Don't run the Docker and dev copies of the same app at once unless you want two writers: both work, but their load and metrics add up.

### Apps in development mode (on the host, not in Docker)

The cluster stays in Docker; the app connects to `localhost:6446/6447`. Prometheus scrapes these too (`mode=dev`).

```bash
# Spring Boot -> http://localhost:8081   (terminal 1, from the repository root)
cd apps/labapp-springboot && mvn spring-boot:run -Dspring-boot.run.profiles=dev

# Go -> http://localhost:8091            (terminal 2, from the repository root)
cd apps/labapp-go && set -a && . ../../.env && set +a
DB_HOST=localhost HTTP_PORT=8091 go run ./cmd/labapp
```

### Status and logs

```bash
docker compose --profile monitoring --profile app ps -a
docker logs --tail 100 -f mysql1                  # a node (also mysql-router1, mysql-router, prometheus …)
docker compose logs --tail 100 -f labapp-springboot labapp-go
```

---

## 3. How to test everything

Run these from the repository root after `source scripts/helpers.sh`. Each block takes under a minute; the lesson linked on the right has the full test and explanation.

### Smoke test: is everything up?

| Check | Command | Expected | Lesson |
|---|---|---|---|
| Cluster | `mstatus` | `"status": "OK"`, 3 members `ONLINE` | [1](docs/01-innodb-cluster.md) |
| Primary / secondary | `q 6446` · `q 6447` | the primary · a secondary | [1](docs/01-innodb-cluster.md) |
| Writes | `tx 1` | `OK` + primary hostname | [0](docs/00-setup.md) |
| Read/write splitting | `r "SELECT @@hostname"` | a secondary | [5](docs/05-consistency.md) |
| Routers | http://localhost:8404/stats | both routers green (UP) | [4](docs/04-multiple-routers.md) |
| Failover settings | `mgrvars` | the values from Lesson 6 on every node | [6](docs/06-failover-tuning.md) |
| Prometheus | http://localhost:9090/targets | `mysql`, `haproxy`, `prometheus` UP; app targets UP for the apps you started (the other `mode` is DOWN, which is expected) | [3](docs/03-monitoring.md) |
| Grafana | http://localhost:3000 | **1 · Overview** shows 3 nodes and a primary | [3](docs/03-monitoring.md) |
| Spring Boot app | `curl -s localhost:8080/whoami \| jq` · `curl -s localhost:8080/health/db \| jq` | writer = primary, reader = secondary · HTTP 200 | [10](docs/10-spring-app.md) |
| Go app | `curl -s localhost:8090/whoami \| jq` · `curl -s localhost:8090/health/db \| jq` | same as Spring Boot | 10b |

### App APIs (both apps, swap the port: 8080 Spring Boot, 8090 Go)

```bash
curl -s -X POST 'localhost:8090/orders?item=pen&qty=2' | jq   # 201, "writtenOn" = primary
curl -s localhost:8090/orders/1 | jq                         # "readFrom" = a secondary
curl -s 'localhost:8090/orders/1?fresh=true' | jq            # "readFrom" = the primary
curl -s 'localhost:8090/orders?limit=5' | jq
K=$(uuidgen | tr A-Z a-z)                                    # idempotency: 201, then 200 + "deduplicated": true
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Idempotency-Key: $K" localhost:8090/orders
curl -s -X POST -H "Idempotency-Key: $K" localhost:8090/orders | jq .deduplicated
```

### Failover with SQL clients (Lessons 2 and 6)

```bash
txloop                                   # terminal 1: one insert per second, also written to tx.log
docker kill $(q 6446)                    # terminal 2: crash the primary
mevents 3m                               # timeline: node lost -> new primary -> routers -> first OK
docker start mysql1 mysql2 mysql3        # the killed node rejoins as SECONDARY
mstatus                                  # back to 3 ONLINE
```

Planned switchover with no failed transactions: `mprimary mysql2`, then `mprimary` (back to mysql1). Network partitions: `mcut mysql3` / `mjoin mysql3` ([Lesson 6.4](docs/06-failover-tuning.md#64-network-partitions)).

### Failover under application load (Lessons 10 and 10b)

```bash
for p in 8080 8090; do curl -s -X POST localhost:$p/load/reset >/dev/null; curl -s -X POST localhost:$p/load/start | jq -c; done
docker kill $(q 6446)
sleep 30; docker start mysql1 mysql2 mysql3
for p in 8080 8090; do curl -s localhost:$p/load/status | jq -c; curl -s localhost:$p/health/db | jq -c '.outages[0]'; done
for p in 8080 8090; do curl -s -X POST localhost:$p/load/stop >/dev/null; done
```

Pass: `"failed": 0` for both apps, one writer outage of about 7 s per app, and the reader path not affected.

### Unit tests and builds

```bash
(cd apps/labapp-go && go vet ./... && go test ./...)      # no database needed
(cd apps/labapp-springboot && mvn test)                   # uses the dev profile: cluster must be running
```

---

## 4. How to clean and restart everything

Name the same profiles as on `up`, otherwise the profiled containers are left out.

| Goal | Command | Data kept? |
|---|---|---|
| Pause | `docker compose --profile monitoring --profile app stop` | ✅ |
| Resume | `docker compose --profile monitoring --profile app up -d` | ✅ |
| Remove containers | `docker compose --profile monitoring --profile app down` | ✅ volumes stay |
| **Full reset** (fresh cluster, empty Prometheus/Grafana data) | `docker compose --profile monitoring --profile app down -v --remove-orphans` | ❌ |
| Full reset + delete built app images | add `--rmi local` to the line above | ❌ |
| Rebuild one app after a code change | `docker compose --profile app-go up -d --build labapp-go` | ✅ |

After a **full reset**, start again as in [§2](#start). `cluster-init` recreates the cluster, users, schemas and `app.t`, and the apps recreate their tables on startup. Nothing is done by hand.

### Back to a clean state after a test

| Situation | Command |
|---|---|
| A killed node | `docker start mysql1 mysql2 mysql3` (rejoins in 10–30 s) |
| Node running but `MISSING` / `OFFLINE` | `docker compose up -d cluster-init` |
| Primary moved to another node | `mprimary` (back to mysql1) |
| After a partition test | `mjoin <node>`, then `mstatus` |
| `memberWeight` changed in Lesson 6.2 | `mjs "cluster.setInstanceOption('mysql3:3306', 'memberWeight', 50)"` |
| Stuck helper containers | `mclean` |
| App load counters | `curl -s -X POST localhost:8080/load/reset` (and `:8090`) |
| Old `txloop` output | `rm tx.log` |
| **Docker or the computer restarted** | `docker compose --profile monitoring --profile app up -d`, then `docker restart mysql-router1 mysql-router2` |

Why the last case needs `up -d`: all nodes start at the same moment, so there is no running group to rejoin. Only `cluster-init` reboots the cluster, and it runs on `docker compose up`, not on Docker's own restarts. More cases and fixes: [Operations](docs/operations.md#restarting).

---

## 5. Where to look for the result

| Question | Where |
|---|---|
| Did the cluster bootstrap? | `docker logs --tail 100 cluster-init` (ends with the cluster status JSON) |
| Cluster health, who is primary | `mstatus`, `members`, `q 6446`; Grafana **1 · Overview** |
| What happened during a failover, in order | `mevents 3m`; Grafana **5 · Failover & Outages** (timelines and annotations) |
| Did SQL clients see errors? | `tx.log` (`OK` / `FAIL` per second from `txloop`) |
| Replication lag, transactions per node | Grafana **2 · MySQL Cluster** |
| Router health, sessions, which router took traffic | http://localhost:8404/stats; Grafana **3 · Routers & HAProxy** |
| App success rate, retries, errors by code, latency | `/load/status`; Grafana **4 · Applications** (choose the `application` at the top) |
| How long the app could not write or read | `/health/db` (`outages`: start, end, ms, first error); Grafana **5 · Failover & Outages** |
| Connection pools (Hikari vs Go `database/sql`) | Grafana **6 · Connection Pools** |
| Raw metrics | `curl -s localhost:8080/actuator/prometheus` (Spring Boot) · `curl -s localhost:8090/metrics` (Go); Prometheus http://localhost:9090 |
| App logs (retries, probe DOWN/UP lines) | `docker compose logs --tail 100 -f labapp-springboot labapp-go` |
| Measured results of each test | the "Results" tables in each lesson, for example [Lesson 6](docs/06-failover-tuning.md#summary) and [Lesson 10](docs/10-spring-app.md#test-primary-crash-with-the-app-under-load) |

Grafana logs in as `admin` with `GRAFANA_ADMIN_PASSWORD` from `.env`. All dashboards are in the **MySQL HA** folder.

| URL | What |
|---|---|
| `localhost:6446` / `localhost:6447` / `localhost:6450` | MySQL read/write / read-only / read-write splitting, through HAProxy |
| http://localhost:8404/stats | HAProxy |
| http://localhost:9090 | Prometheus |
| http://localhost:3000 | Grafana → **1 · Overview** (home), 2 · Cluster, 3 · Routers, 4 · Applications, 5 · Failover, 6 · Pools |
| http://localhost:8080 / :8081 | Spring Boot app (Docker / dev) |
| http://localhost:8090 / :8091 | Go app (Docker / dev) |

---

## Project structure

```
.
├── README.md
├── docs/                     # lessons and operations
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
├── apps/
│   ├── labapp-springboot/    # Spring Boot lab app (Java 25)        → Lesson 10
│   └── labapp-go/            # Go lab app, same API and metrics      → Lesson 10b
├── haproxy/
│   └── haproxy.cfg           # load balancer for the two routers     → Lesson 4
└── monitoring/
    ├── init-exporter-user.sh # creates the read-only exporter user   → Lesson 3
    ├── prometheus.yml        # scrape config                         → Lesson 3
    └── grafana/
        ├── provisioning/     # datasource + dashboard provider       → Lesson 3
        └── dashboards/       # 1-overview … 6-connection-pools.json → Lessons 3, 4, 10
```

---

## License

MIT
