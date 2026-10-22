#!/usr/bin/env bash
# Deploys the current origin/main on this server. Run it from anywhere:
#
#   ~/agent-harness-go/deploy/ec2/deploy.sh
#
# Roll back by deploying an earlier commit:
#
#   DEPLOY_REF=<previous sha> deploy/ec2/deploy.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

ref="${DEPLOY_REF:-origin/main}"
compose=(docker compose
  -f deploy/docker-compose.yml
  -f deploy/ec2/docker-compose.caddy.yml
  --env-file .env)

[[ -f .env ]] || { echo "missing .env (copy .env.example and fill it in)" >&2; exit 1; }

git fetch --quiet origin
git checkout --quiet --detach "$ref"
echo "deploying $(git rev-parse --short HEAD): $(git log -1 --format=%s)"

"${compose[@]}" up -d --build --remove-orphans

# Wait for readiness. The harness image has no wget/curl and its port is not
# published, so ask through the caddy container, which reaches it by name.
ready=false
for _ in $(seq 1 30); do
  if "${compose[@]}" exec -T caddy wget -q -O /dev/null http://harness:8080/readyz 2>/dev/null; then
    ready=true
    break
  fi
  sleep 2
done

"${compose[@]}" ps
docker image prune -f >/dev/null

if [[ "$ready" != true ]]; then
  echo "harness did not become ready; see: ${compose[*]} logs harness" >&2
  exit 1
fi
echo "ready"
