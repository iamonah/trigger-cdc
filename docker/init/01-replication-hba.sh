#!/bin/bash
set -euo pipefail

# Allow replication connections from outside the container. The initdb default
# only trusts replication from the container's own loopback interface, while
# clients connecting through the published port arrive from the Docker bridge.
if ! grep -q '^host replication all all' "${PGDATA:?}/pg_hba.conf"; then
    echo "host replication all all scram-sha-256" >> "${PGDATA}/pg_hba.conf"
fi
