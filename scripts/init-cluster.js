const NODES   = ['mysql1', 'mysql2', 'mysql3'];
const ROOT_PW = os.getenv('MYSQL_ROOT_PASSWORD');
const ADM     = os.getenv('CLUSTER_ADMIN');
const ADM_PW  = os.getenv('CLUSTER_ADMIN_PASSWORD');
const NAME    = os.getenv('CLUSTER_NAME') || 'vivekCluster';

// 1. Configure each node once (skip if cluster admin already exists)
for (const h of NODES) {
  try {
    shell.connect(`${ADM}:${ADM_PW}@${h}:3306`);
    println(`${h}: already configured`);
  } catch (e) {
    println(`${h}: configuring...`);
    dba.configureInstance(`root:${ROOT_PW}@${h}:3306`, {
      clusterAdmin: ADM, clusterAdminPassword: ADM_PW, restart: false
    });
  }
}

// 2. Find, reboot, or create the cluster
shell.connect(`${ADM}:${ADM_PW}@mysql1:3306`);
let cluster;
try {
  cluster = dba.getCluster();
  println('Cluster is online');
} catch (e) {
  const hasMetadata = shell.getSession().runSql(
    "SELECT COUNT(*) FROM information_schema.schemata " +
    "WHERE schema_name = 'mysql_innodb_cluster_metadata'"
  ).fetchOne()[0] > 0;

  if (hasMetadata) {
    println('Cluster metadata found but group is offline: rebooting...');
    cluster = dba.rebootClusterFromCompleteOutage();
  } else {
    println('No cluster found: creating...');
    cluster = dba.createCluster(NAME);
  }
}

// 3. Add missing nodes, rejoin offline ones
const topo = cluster.status().defaultReplicaSet.topology;
for (const h of NODES) {
  const key = `${h}:3306`;
  if (!topo[key]) {
    println(`Adding ${h}...`);
    cluster.addInstance(`${ADM}:${ADM_PW}@${key}`, { recoveryMethod: 'clone' });
  } else if (topo[key].status !== 'ONLINE') {
    println(`Rejoining ${h}...`);
    cluster.rejoinInstance(`${ADM}:${ADM_PW}@${key}`);
  }
}

// 4. Failover tuning (Lesson 6). Re-applied on every run, so a fresh cluster
//    (docker compose down -v) gets the same behaviour. All values are persisted
//    with SET PERSIST and survive restarts.
const EXPEL_TIMEOUT        = 0;   // expel 0 s after a member is suspected (default 5)
const UNREACHABLE_MAJORITY = 10;  // a cut-off minority leaves after 10 s instead of hanging (default 0 = never)
const AUTO_REJOIN_TRIES    = 3;   // expelled members try to rejoin by themselves (default 3)

try {
  cluster.setOption('expelTimeout', EXPEL_TIMEOUT);
  cluster.setOption('autoRejoinTries', AUTO_REJOIN_TRIES);
  for (const h of NODES) {
    shell.connect(`root:${ROOT_PW}@${h}:3306`);
    shell.getSession().runSql(
      `SET PERSIST group_replication_unreachable_majority_timeout = ${UNREACHABLE_MAJORITY}`);
  }
  shell.connect(`${ADM}:${ADM_PW}@mysql1:3306`);
  println(`Tuning applied: expelTimeout=${EXPEL_TIMEOUT}, autoRejoinTries=${AUTO_REJOIN_TRIES}, ` +
          `unreachable_majority_timeout=${UNREACHABLE_MAJORITY}`);
} catch (e) {
  println(`Tuning NOT applied (is every member ONLINE?): ${e.message}`);
}

// 5. Application schemas and users. Re-applied on every run (safe to repeat): creates what is
//    missing and re-syncs passwords with .env. Runs on the PRIMARY; Group Replication copies it.
//    Each account only gets rights on its own schema (least privilege).
const APP_ACCOUNTS = [
  // [schema,             user env var,                 password env var]
  ['app',               'APP_USER',                   'APP_PASSWORD'],                  // shell helpers (m, w, tx)
  ['labapp_springboot', 'LABAPP_SPRINGBOOT_DB_USER',  'LABAPP_SPRINGBOOT_DB_PASSWORD'], // Lesson 10, Spring Boot
  ['labapp_go',         'LABAPP_GO_DB_USER',          'LABAPP_GO_DB_PASSWORD'],         // Lesson 10, Go
];
const APP_PRIVILEGES = 'SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, INDEX, ALTER';

const ident = v => { if (!/^[A-Za-z0-9_]+$/.test(v)) throw new Error(`invalid name: ${v}`); return v; };
const quote = v => "'" + String(v).replace(/\\/g, '\\\\').replace(/'/g, "\\'") + "'";

try {
  const primary = cluster.status().defaultReplicaSet.primary;      // e.g. "mysql1:3306"
  shell.connect(`root:${ROOT_PW}@${primary}`);
  const s = shell.getSession();
  for (const [schema, userVar, pwVar] of APP_ACCOUNTS) {
    const user = os.getenv(userVar), pw = os.getenv(pwVar);
    if (!user || !pw) { println(`App account ${schema}: skipped (${userVar}/${pwVar} not set)`); continue; }
    const u = `${quote(ident(user))}@'%'`;
    s.runSql(`CREATE DATABASE IF NOT EXISTS ${ident(schema)}`);
    s.runSql(`CREATE USER IF NOT EXISTS ${u} IDENTIFIED BY ${quote(pw)}`);
    s.runSql(`ALTER USER ${u} IDENTIFIED BY ${quote(pw)}`);
    s.runSql(`GRANT ${APP_PRIVILEGES} ON ${ident(schema)}.* TO ${u}`);
    println(`App account ${schema}: user ${user} ready`);
  }
  // test table of the shell helpers (txloop, w); the lab apps create their own tables
  s.runSql('CREATE TABLE IF NOT EXISTS app.t (id INT AUTO_INCREMENT PRIMARY KEY, v INT, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)');
  shell.connect(`${ADM}:${ADM_PW}@mysql1:3306`);
} catch (e) {
  println(`App accounts NOT created (is there a primary?): ${e.message}`);
}

print(JSON.stringify(cluster.status(), null, 2));