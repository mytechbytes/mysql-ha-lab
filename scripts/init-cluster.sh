#!/bin/bash
set -e

# Wait until every node accepts TCP connections.
# The image's temporary init server uses --skip-networking,
# so this only succeeds once the real server is up.
for h in mysql1 mysql2 mysql3; do
  until mysqladmin ping -h "$h" -uroot -p"$MYSQL_ROOT_PASSWORD" --silent 2>/dev/null; do
    echo "Waiting for $h..."
    sleep 3
  done
  echo "$h is up"
done

export LC_ALL=C.UTF-8 LANG=C.UTF-8
mysqlsh --no-wizard --js --uri "root:${MYSQL_ROOT_PASSWORD}@mysql1:3306" --file /scripts/init-cluster.js