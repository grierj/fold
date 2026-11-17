# fold-backup: nightly and on-demand backups, offsite copies and the uploads
# helpers that restore.sh drives. No Python and no Docker CLI. The host keeps its
# own copies of older archives (deploy/digitalocean/checks.sh); nothing here
# reads or writes them.
#
# Base: the exact PostgreSQL major the stack runs (postgres:16-alpine), pinned by
# the multi-architecture index digest below (Alpine 3.24, PostgreSQL 16.16), so
# pg_dump matches the server and a rebuild cannot change the base silently.
# To update, see "Updating the base and the packages" in README.md.
FROM postgres:16-alpine@sha256:81e77a4833487b8132fe4b00bedd39dc3f74b8641e387f19312a48f7de218c2e

# Exact package versions from the Alpine 3.24 main and community repositories.
# coreutils supplies mv --exchange (renameat2 RENAME_EXCHANGE); flock comes with
# util-linux 2.42 as its own package. A version that left the repository fails
# the build, which is the point.
RUN apk add --no-cache \
      bash=5.3.9-r1 \
      coreutils=9.11-r0 \
      findutils=4.10.0-r1 \
      tar=1.35-r5 \
      gzip=1.15-r0 \
      util-linux=2.42.3-r1 \
      flock=2.42.3-r1 \
      tzdata=2026f-r0 \
      age=1.3.2-r1 \
      rclone=1.75.2-r0

# Fail the build when a promised tool is missing or a forbidden one is present.
RUN set -eu; \
    for t in bash age rclone pg_dump pg_restore psql tar gzip flock sha256sum find od; do command -v "$t" >/dev/null; done; \
    mv --help | grep -q -- '--exchange'; \
    test -f /usr/share/zoneinfo/UTC; \
    if [ -n "$(find /bin /usr/bin /usr/local/bin -name 'pyth*' -print -quit)" ]; then echo "an interpreter that does not belong here is installed" >&2; exit 1; fi; \
    if command -v docker >/dev/null 2>&1; then echo "a container client is installed" >&2; exit 1; fi

# Fresh named volumes copy these folders' ownership on first use, so the
# nonroot service can write them whichever container creates the volume first.
# /backups is root:65532 mode 1770: the service (group 65532) creates and removes
# its own top-level archives, and cannot remove or replace anything the host
# (root) keeps there, such as the host-owned adopted/ folder (mode 0700).
# /backup-status holds LAST_OK and status.json.
RUN mkdir -p /backups /backup-status /data/uploads \
 && chown 0:65532 /backups \
 && chown 65532:65532 /backup-status /data /data/uploads \
 && chmod 1770 /backups \
 && chmod 755 /backup-status /data /data/uploads

COPY --chmod=0755 backup.sh /usr/local/bin/backup.sh

ENV HOME=/tmp
# The base image stops PostgreSQL with SIGINT; backup.sh handles both.
STOPSIGNAL SIGTERM
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/backup.sh"]
CMD ["loop"]
