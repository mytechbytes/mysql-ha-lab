# Lesson 10: A Spring Boot application on the cluster

[← Lesson 6: Failover tuning](06-failover-tuning.md) · [README](../README.md) · Next: [Operations →](operations.md)

Branch: `10-spring-app`

Until now every failover was watched from the database side (`txloop`, `mevents`) and every crash
cost several seconds of `FAIL` lines. This lesson adds a real application, `labapp-springboot`,
and asks: **what does an application experience during a failover, and what must it do so its
users don't notice?**

```
                     ┌──────────────── labapp-springboot ────────────────┐
 curl / load loop ─► │ REST /orders, /whoami, /health/db, /load/*        │
                     │ OrderService ── Retry (whole transaction)         │
                     │   readOnly? yes ─► reader pool ───────────────────┼─► HAProxy :6447 ─► Router ─► SECONDARY
                     │             no  ─► writer pool ───────────────────┼─► HAProxy :6446 ─► Router ─► PRIMARY
                     │ DbProbe (1 s) · /actuator/prometheus              │─► Prometheus ─► Grafana
                     └───────────────────────────────────────────────────┘
```

A second lab app in Go (`apps/labapp-go`) follows the same conventions: own schema, own user,
same API and metric names, so both can run side by side and share the dashboard.

## What is in this lesson

| Path | Purpose |
|---|---|
| `apps/labapp-springboot/` | Spring Boot 4.1, Java 25, Maven |
| `.../db/DataSourceConfig.java` | Two HikariCP pools (`springboot-writer` → 6446, `springboot-reader` → 6447) bound from YAML, read/write routing, `writeTx` / `readTx` |
| `.../db/ReadWriteRoutingDataSource.java` | Read-only transaction → reader pool, everything else → writer pool |
| `.../db/Retry.java` | Retries a whole transaction on failover errors; metrics per operation and per failed attempt |
| `.../web/DbErrorHandler.java` | Any controller: a DB error left after retries → `503` with the MySQL error text |
| `.../orders/*` | Orders API, table creation at startup, background load loop |
| `.../health/*` | `DbProbe`: one write + one read per second, records every outage (start, end, duration); `/health/db`; `/whoami` |
| `.../resources/application.yaml` | Pools, timeouts, retry, load, probe, metrics |
| `.../resources/application-dev.yml` | Dev profile: app on the Mac, cluster via `localhost:6446/6447`, reads `../../.env` |
| `docker-compose.yml` | Services `labapp-springboot` (profiles `app`, `app-springboot`) and `labapp-go` (`app`, `app-go`) |
| `monitoring/prometheus.yml` | Jobs `labapp-springboot`, `labapp-go` (Docker and dev-mode targets) |
| `monitoring/grafana/dashboards/4-…`, `5-…`, `6-….json` | Dashboards *4 · Applications*, *5 · Failover & Outages*, *6 · Connection Pools* |

## Setup

### 1. `.env`

Each lab app has its own image name, MySQL user and password (`.env.example` has placeholders):

```bash
# Lab apps
LABAPP_SPRINGBOOT_IMAGE=mysql-ha-labapp-springboot:latest
LABAPP_SPRINGBOOT_DB_USER=labapp_springboot
LABAPP_SPRINGBOOT_DB_PASSWORD=change-me

LABAPP_GO_IMAGE=mysql-ha-labapp-go:latest
LABAPP_GO_DB_USER=labapp_go
LABAPP_GO_DB_PASSWORD=change-me
```

Use plain `KEY=value`, no quotes: the dev profile reads `.env` as a properties file, where quotes
would become part of the password.

### 2. Database, user and password

One schema and one user **per app**. **`cluster-init` creates them automatically** on every run
(`scripts/init-cluster.js`, step 5) from the `.env` values above, on the primary; Group Replication
copies them to every node. So after setting `.env`:

```bash
docker compose up -d cluster-init
docker logs --tail 100 cluster-init | grep 'App account'
# App account app: user app ready
# App account labapp_springboot: user labapp_springboot ready
# App account labapp_go: user labapp_go ready
```

Run it again after changing a password in `.env`: it re-syncs the password (`ALTER USER`).

**Manual fallback** (what step 5 does, if you ever need it by hand):

```bash
source scripts/helpers.sh

mroot -e "
  CREATE DATABASE IF NOT EXISTS labapp_springboot;
  CREATE USER IF NOT EXISTS '$LABAPP_SPRINGBOOT_DB_USER'@'%' IDENTIFIED BY '$LABAPP_SPRINGBOOT_DB_PASSWORD';
  ALTER USER '$LABAPP_SPRINGBOOT_DB_USER'@'%' IDENTIFIED BY '$LABAPP_SPRINGBOOT_DB_PASSWORD';
  GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, INDEX, ALTER
    ON labapp_springboot.* TO '$LABAPP_SPRINGBOOT_DB_USER'@'%';

  CREATE DATABASE IF NOT EXISTS labapp_go;
  CREATE USER IF NOT EXISTS '$LABAPP_GO_DB_USER'@'%' IDENTIFIED BY '$LABAPP_GO_DB_PASSWORD';
  ALTER USER '$LABAPP_GO_DB_USER'@'%' IDENTIFIED BY '$LABAPP_GO_DB_PASSWORD';
  GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, INDEX, ALTER
    ON labapp_go.* TO '$LABAPP_GO_DB_USER'@'%';"
```

| Statement | Why |
|---|---|
| `CREATE DATABASE` | Own schema: the apps never see each other's data or the `app` schema of the shell helpers |
| `CREATE USER IF NOT EXISTS` + `ALTER USER` | Safe to rerun; `ALTER` re-syncs the password with `.env` after you change it |
| `GRANT ... ON labapp_springboot.*` | Least privilege: data and DDL on its own schema only. `CREATE` is needed because the app creates its tables at startup |

Check:

```bash
mroot -e "SELECT user, host FROM mysql.user WHERE user LIKE 'labapp%'"
mroot -e "SHOW GRANTS FOR '$LABAPP_SPRINGBOOT_DB_USER'@'%'"
```

The tables (`orders`, `heartbeat`) are **not** created by hand: `SchemaInit` creates them on the
primary when the app starts.

### 3. Run in development mode (on the Mac)

Requires Java 25 and Maven (for example via SDKMAN). The `dev` profile connects to
`localhost:6446` / `localhost:6447` (published by HAProxy) and imports `../../.env`.

```bash
cd apps/labapp-springboot
mvn spring-boot:run -Dspring-boot.run.profiles=dev      # http://localhost:8081
```

IntelliJ: Run configuration for `LabappApplication`, *Active profiles* `dev`,
*Working directory* `.../mysql-ha/apps/labapp-springboot` (so `../../.env` resolves).

### 4. Run in Docker (on the cluster network)

```bash
docker compose --profile app-springboot build labapp-springboot
docker compose --profile app-springboot up -d labapp-springboot
docker logs --tail 100 -f labapp-springboot              # "table orders ready", "Started ..."
```

The container uses the default profile: `mysql-router:6446/6447`, user and password from
`LABAPP_SPRINGBOOT_DB_USER` / `_PASSWORD` via Compose. Port 8080.

### 5. Verify

```bash
curl -s localhost:8081/whoami          # writer = primary (read_only 0), reader = a secondary (read_only 1)
curl -s localhost:8081/health/db       # writer and reader up, no outages
curl -s -X POST 'localhost:8081/orders?item=book&qty=2'
curl -s 'localhost:8081/orders?limit=5'
```

Which app holds which connections, per node (`program_name` is set as a connection attribute):

```bash
for n in mysql1 mysql2 mysql3; do
  mroot -h $n -P 3306 -N -e "SELECT @@hostname, ATTR_VALUE, COUNT(*) FROM performance_schema.session_connect_attrs
                             WHERE ATTR_NAME = 'program_name' AND ATTR_VALUE LIKE 'labapp%' GROUP BY ATTR_VALUE"
done
```

## Build and tests

```bash
mvn package -DskipTests        # just the jar (what the Docker build does)
mvn test                       # needs the cluster running: the test starts the app with the dev profile
```

`LabappApplicationTests` is an integration test: it starts the whole application against the
cluster with the `dev` profile. Without the cluster it fails with a pool timeout; use
`-DskipTests`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `jdbcDialect ... springboot-writer - Connection is not available, request timed out after 3001ms (total=0)` during `mvn install` | Spring Data JDBC (pulled in by `spring-boot-starter-data-jdbc-test`) opens a connection at startup to detect the dialect, and the test used the default profile, whose host `mysql-router` does not resolve on the Mac | Remove `spring-boot-starter-data-jdbc-test` (the app uses `JdbcClient`, not Spring Data); the test runs with `@ActiveProfiles("dev")` |
| `Access denied for user ''@...` | `.env` not found, user empty | Working directory must be `apps/labapp-springboot`; check `spring.config.import` in `application-dev.yml` |
| `Access denied for user 'labapp_springboot'@...` (ERROR 1045, fails at once, not retried) | User missing or password differs from `.env` | `docker compose up -d cluster-init` (creates the user and re-syncs the password) |
| `Unknown database 'labapp_springboot'` (ERROR 1049) | Schema not created | `docker compose up -d cluster-init` |
| All writes take ~30 s and fail, probes DOWN, `firstError` shows `pool-timeout ... cause: ...` | The pool cannot open any connection; the cause is after `cause:` | Fix that cause (host, user, schema); configuration errors like 1045/1049 are no longer retried |
| `docker compose --profile app ...` fails building `labapp-go` | `--profile app` includes both apps | Use `--profile app-springboot` until `apps/labapp-go` has its Dockerfile |

## Monitoring

The app shows up in four dashboards (Grafana → *MySQL HA*; the **Application** dropdown selects
`labapp-springboot`, `labapp-go` or both):

| Dashboard | What you see for the app |
|---|---|
| 1 · Overview | Write / read path up, success %, failed ops, last outage duration |
| 4 · Applications | Did users notice (success %, failed vs retried) and why (errors by MySQL code, latency, stale reads) |
| 5 · Failover & Outages | Node → primary → router → app timelines on one screen, plus the outage report (start, end, duration) |
| 6 · Connection Pools | Hikari writer / reader pools: active, idle, pending, timeouts |

Metrics used: `app_db_ops_total`, `app_db_attempt_errors_total`, `app_db_op_seconds`, `app_db_stale_reads_total`,
`app_db_probe_up`, `app_db_outage_last_*_seconds`, `app_db_outage_seconds_count`, `hikaricp_*` (Go: `go_sql_*`).

Prometheus has two targets per app, one for each way of running it:

| App runs | Target | Shown as |
|---|---|---|
| In Docker (`--profile app-springboot`) | `labapp-springboot:8080` | `mode="docker"` |
| On the Mac, dev profile | `host.docker.internal:8081` | `mode="dev"` |

The one that is not running is simply `DOWN` in *Status → Targets*. After changing
`prometheus.yml` or `docker-compose.yml`:

```bash
docker compose --profile monitoring up -d prometheus      # picks up extra_hosts and the new config
docker compose --profile monitoring up -d grafana         # new home dashboard + the 6 dashboard files
```

## Next

- 10.2 planned switchover and 10.3 primary crash with the load loop running
- 10.4 lost commit (dedup), 10.5 stale reads, 10.6 router failure
