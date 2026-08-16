# PostgreSQL 17 with pg_partman for time-series partition maintenance.
# Version is pinned: partition layout must not change under us on a rebuild.
FROM postgres:17-trixie

ARG PARTMAN_VERSION=5.5.0-1.pgdg13+1

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        "postgresql-17-partman=${PARTMAN_VERSION}" \
 && rm -rf /var/lib/apt/lists/*

# pg_partman's background worker must be preloaded at server start. It runs as
# partman_maintainer (a non-superuser role created by migration 00002); pinning
# it to a superuser is what CVE-2026-61821 and its siblings were about.
CMD ["postgres", \
     "-c", "shared_preload_libraries=pg_partman_bgw", \
     "-c", "pg_partman_bgw.interval=3600", \
     "-c", "pg_partman_bgw.role=partman_maintainer", \
     "-c", "pg_partman_bgw.dbname=hoodalpha", \
     "-c", "timezone=UTC"]
