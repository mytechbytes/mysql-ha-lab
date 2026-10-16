#!/usr/bin/env bash

set -euo pipefail
# It will wait for the router to be available before proceeding.
until mysql -h mysql-router -P 6446 -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SELECT 1" >/dev/null 2>&1; do
  echo "Waiting for router..."; sleep 3
done
# Create the exporter user, with a maximum of 5 connections to prevent overloading the server, with the necessary privileges for monitoring.
# The ALTER USER command is used to ensure that the password is set correctly, even if the user already exists.
# The user is granted PROCESS, REPLICATION CLIENT, and SELECT privileges on all databases and ability to connect from any host ('%').
mysql -h mysql-router -P 6446 -uroot -p"$MYSQL_ROOT_PASSWORD" 2>/dev/null <<SQL
CREATE USER IF NOT EXISTS '${EXPORTER_USER}'@'%' 
  IDENTIFIED WITH caching_sha2_password BY '${EXPORTER_PASSWORD}' 
  WITH MAX_USER_CONNECTIONS 5;

ALTER USER '${EXPORTER_USER}'@'%' 
  IDENTIFIED WITH caching_sha2_password BY '${EXPORTER_PASSWORD}';

GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO '${EXPORTER_USER}'@'%';
SQL
echo "Exporter user '${EXPORTER_USER}' is ready for MySQL 8.4."
