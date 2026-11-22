#!/usr/bin/env bash
set -euo pipefail

# This check owns the local Compose project and assumes an isolated Docker host.
test -z "$(docker ps --all --quiet --filter label=com.docker.compose.project=dataground-openshell-local-v019)"

GOMODCACHE=$(go env GOMODCACHE)
GOCACHE=$(go env GOCACHE)
export GOMODCACHE GOCACHE
temporary_home="$(mktemp -d)"
export HOME="$temporary_home"
unset XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME
gateway=http://127.0.0.1:8080
workload=$(jq -er .images.workload deploy/openshell/local/release.json)
version=$(jq -er .version deploy/openshell/local/release.json)
profile=deploy/openshell/local/providers/dataground-codex-chatgpt.yaml
provider=dg-release-synthetic
sandbox=dg-release-smoke
cli=(openshell --gateway-endpoint "$gateway" --workspace default)

cleanup() {
  local status=$?
  trap - EXIT
  if (( status != 0 )); then
    docker ps --all --filter label=openshell.sandbox_namespace=dataground-local-v019
    docker logs --tail 100 dataground-openshell-local-v019-gateway-1 || true
    "${cli[@]}" sandbox delete "$sandbox" || true
    "${cli[@]}" provider delete "$provider" || true
  fi
  node scripts/openshell-local.mjs down || status=1
  rm -rf -- "$temporary_home"
  exit "$status"
}
trap cleanup EXIT

node scripts/check-openshell-profile.mjs
node scripts/openshell-local.mjs up
ready=false
for _ in {1..60}; do
  if curl --fail --silent http://127.0.0.1:8081/readyz >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
test "$ready" = true
"${cli[@]}" status --output json |
  jq -e --arg endpoint "$gateway" --arg version "$version" \
    '.status == "connected" and .server == $endpoint and .version == $version'

"${cli[@]}" provider profile lint --file "$profile"
"${cli[@]}" provider profile import --file "$profile"
CHATGPT_ACCOUNT_ID=synthetic-account OPENAI_API_KEY=synthetic-token "${cli[@]}" \
  provider create --name "$provider" --type dataground-codex-chatgpt \
  --credential CHATGPT_ACCOUNT_ID --credential OPENAI_API_KEY
"${cli[@]}" provider list --output json --page-size 1 |
  jq -e --arg name "$provider" \
    'any(.providers[]; .name == $name and .workspace == "default"
      and .credential_keys == ["CHATGPT_ACCOUNT_ID", "OPENAI_API_KEY"])'

"${cli[@]}" sandbox create --name "$sandbox" --from "$workload" \
  --no-auto-providers --approval-mode manual --policy deploy/openshell/policies/deny-all.yaml \
  --provider "$provider" --label dataground-release-test=true --detach -- /bin/sleep infinity
"${cli[@]}" sandbox list --output json --page-size 1 --selector dataground-release-test=true |
  jq -e --arg name "$sandbox" \
    'any(.sandboxes[]; .name == $name and .workspace == "default" and .phase == "Ready")'
output=$(printf '%s\n' release-stdio | "${cli[@]}" sandbox exec "$sandbox" \
  --no-tty --no-login-shell --timeout 20 -- /bin/cat)
test "$output" = release-stdio

"${cli[@]}" sandbox exec "$sandbox" --no-tty --no-login-shell --timeout 20 -- \
  /bin/sh -c 'printf "%s\n" retained > /sandbox/dataground-release-fixture'
"${cli[@]}" sandbox stop "$sandbox"
"${cli[@]}" sandbox get "$sandbox" --output json | jq -e '.phase == "Stopped"'
"${cli[@]}" sandbox start "$sandbox"
output=$("${cli[@]}" sandbox exec "$sandbox" --no-tty --no-login-shell --timeout 20 -- \
  /bin/cat /sandbox/dataground-release-fixture)
test "$output" = retained

"${cli[@]}" sandbox delete "$sandbox"
"${cli[@]}" sandbox list --output json --selector dataground-release-test=true |
  jq -e '.sandboxes == [] and .next_page_token == ""'
DATAGROUND_TEST_OPENSHELL_RELEASE_ROOT="$PWD" \
  DATAGROUND_TEST_OPENSHELL_PROVIDER="$provider" \
  DATAGROUND_TEST_OPENSHELL_WORKLOAD="$workload" \
  go test ./internal/execution/openshell -run '^TestWorkspaceCLILiveLifecycle$' -count=1 -timeout=5m

"${cli[@]}" provider delete "$provider"
"${cli[@]}" provider list --output json | jq -e '.providers == [] and .next_page_token == ""'
test -z "$(docker ps --all --quiet --filter label=openshell.sandbox_namespace=dataground-local-v019)"
