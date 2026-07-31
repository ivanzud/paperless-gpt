#!/bin/sh
set -e

# Use environment variables PUID/PGID, otherwise default to 10001
PUID=${PUID:-10001}
PGID=${PGID:-10001}

# Validate PUID/PGID before using numeric test operators.
case "${PUID}" in
    ''|*[!0-9]*)
        echo "ERROR: PUID must be a positive integer (got PUID=${PUID})"
        exit 1
        ;;
esac
case "${PGID}" in
    ''|*[!0-9]*)
        echo "ERROR: PGID must be a positive integer (got PGID=${PGID})"
        exit 1
        ;;
esac
if [ "${PUID}" -eq 0 ] || [ "${PGID}" -eq 0 ]; then
    echo "ERROR: PUID and PGID must be positive, non-root IDs (got PUID=${PUID}, PGID=${PGID})"
    exit 1
fi

# Create group/user entries when the requested IDs are not taken yet.
# A GID/UID that already exists (e.g. Alpine ships group "users" with GID 100,
# which Unraid requires as PGID, see #995) is simply reused — privileges are
# dropped by numeric ID below, so a pre-existing entry with a different name
# works just as well. Creation is best-effort: a name clash from a previous
# container start with different IDs must not prevent startup either.
if ! getent group "${PGID}" >/dev/null 2>&1; then
    addgroup -g "${PGID}" paperless-gpt \
        || echo "WARN: could not create group paperless-gpt (GID ${PGID}); continuing with numeric GID"
fi

if ! getent passwd "${PUID}" >/dev/null 2>&1; then
    GROUP_NAME=$(getent group "${PGID}" | cut -d: -f1)
    adduser -D -H -S -h /home/paperless-gpt -s /sbin/nologin -G "${GROUP_NAME:-nogroup}" -u "${PUID}" paperless-gpt \
        || echo "WARN: could not create user paperless-gpt (UID ${PUID}); continuing with numeric UID"
fi

# Create necessary directories
umask 027
mkdir -p /app/prompts /app/config /app/db /home/paperless-gpt

# Only mutable state belongs to the runtime account. The executable,
# entrypoint, and bundled defaults remain root-owned and non-writable.
for MUTABLE_DIR in /app/prompts /app/config /app/db /home/paperless-gpt; do
    # Reclaim the mount root first so restarts work with only CHOWN, SETUID,
    # and SETGID capabilities even when the previous run left it mode 0750.
    chown 0:0 "${MUTABLE_DIR}"
    chmod 0750 "${MUTABLE_DIR}"
    chown -R "${PUID}:${PGID}" "${MUTABLE_DIR}"
done

# Drop privileges and execute the main application. Numeric uid:gid makes
# su-exec apply exactly the requested IDs, independent of which passwd/group
# entry (if any) they resolve to. HOME is set after the drop (via env) because
# su-exec overwrites it with the home dir of whatever passwd entry the UID
# resolves to (e.g. "/" for a pre-existing nobody/65534).
echo "Starting application as ${PUID}:${PGID}"
exec su-exec "${PUID}:${PGID}" env HOME=/home/paperless-gpt /app/paperless-gpt
