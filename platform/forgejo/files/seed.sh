#!/bin/sh
# Seeds the GitOps repository once: creates the org ops and copies SEED_REPOSITORY into ops/homelab.
# Never changes an existing org or repository: after the seed, Forgejo is the source of truth.
set -eu

base=http://forgejo-http:3000
api="${base}/api/v1"

# status <curl args...>: prints the HTTP status, authenticated as the local admin
status() {
    curl --silent --output /dev/null --write-out '%{http_code}' --max-time 600 \
        --user "${FORGEJO_ADMIN_USERNAME}:${FORGEJO_ADMIN_PASSWORD}" "$@"
}

until curl --silent --fail --output /dev/null "${base}/api/healthz"; do
    echo "waiting for Forgejo"
    sleep 5
done

case "$(status "${api}/orgs/ops")" in
    200) echo "org ops exists" ;;
    404)
        code="$(status --request POST --header 'Content-Type: application/json' \
            --data '{"username": "ops", "visibility": "private"}' "${api}/orgs")"
        [ "${code}" = 201 ] || { echo "creating org ops: HTTP ${code}" >&2; exit 1; }
        echo "created org ops"
        ;;
    *) echo "checking org ops: unexpected status" >&2; exit 1 ;;
esac

case "$(status "${api}/repos/ops/homelab")" in
    200) echo "ops/homelab exists" ;;
    404)
        code="$(status --request POST --header 'Content-Type: application/json' \
            --data "{\"clone_addr\": \"${SEED_REPOSITORY}\", \"repo_owner\": \"ops\", \"repo_name\": \"homelab\", \"service\": \"git\", \"mirror\": false, \"private\": true}" \
            "${api}/repos/migrate")"
        [ "${code}" = 201 ] || { echo "copying ${SEED_REPOSITORY}: HTTP ${code}" >&2; exit 1; }
        echo "copied ${SEED_REPOSITORY} into ops/homelab"
        ;;
    *) echo "checking ops/homelab: unexpected status" >&2; exit 1 ;;
esac
