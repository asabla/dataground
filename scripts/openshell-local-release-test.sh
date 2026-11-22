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
strict_sandbox=dg-release-strict
foreground=dg-0123456789abcdef
provider_free=dg-release-no-auth
cli=(openshell --gateway-endpoint "$gateway" --workspace default)
started=false

cleanup() {
  local status=$?
  trap - EXIT
  if (( status != 0 )); then
    docker ps --all --filter label=openshell.sandbox_namespace=dataground-local-v019
    docker logs --tail 100 dataground-openshell-local-v019-gateway-1 || true
    if [[ "$started" == true ]]; then
      "${cli[@]}" sandbox delete "$sandbox" "$strict_sandbox" "$foreground" "$provider_free" || true
      "${cli[@]}" provider delete "$provider" || true
    fi
  fi
  node scripts/openshell-local.mjs down || status=1
  rm -rf -- "$temporary_home"
  exit "$status"
}
trap cleanup EXIT

node scripts/check-openshell-profile.mjs
node scripts/openshell-local.mjs up
started=true
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

CHATGPT_ACCOUNT_ID=synthetic-account OPENAI_API_KEY=synthetic-token "${cli[@]}" \
  provider update "$provider" --credential CHATGPT_ACCOUNT_ID --credential OPENAI_API_KEY
"${cli[@]}" provider list --output json --page-size 1 |
  jq -e --arg name "$provider" 'any(.providers[]; .name == $name and .resource_version > 1)'

# Exercise the foreground create and auto-cleanup path used by the local Codex smoke.
output=$("${cli[@]}" sandbox create --name "$foreground" --from "$workload" \
  --no-auto-providers --approval-mode manual --policy deploy/openshell/policies/deny-all.yaml \
  --provider "$provider" --label dataground-release-test=true --no-tty --no-keep \
  -- /usr/bin/printf '%s\n' foreground-exit)
if ! grep -Fxq foreground-exit <<<"$output"; then
  echo "the foreground sandbox command did not emit its expected output" >&2
  exit 1
fi
"${cli[@]}" sandbox list --output json --selector dataground-release-test=true |
  jq -e '.sandboxes == [] and .next_page_token == ""'

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
# Exercise the file-device Landlock rule that required a patched 0.0.86 supervisor.
strict_policy=deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml
"${cli[@]}" sandbox create --name "$strict_sandbox" --from "$workload" \
  --no-auto-providers --approval-mode manual --policy "$strict_policy" \
  --provider "$provider" --label dataground-release-test=true --detach -- /bin/sleep infinity
# Shell expressions must expand inside the sandbox.
# shellcheck disable=SC2016
output=$("${cli[@]}" sandbox exec "$strict_sandbox" --no-tty --no-login-shell --timeout 20 -- \
  /bin/sh -ec '
    test "$(id -un)" = sandbox
    test "$(id -gn)" = sandbox
    printf "%s\n" device-write >/dev/null
    cat /dev/null >/sandbox/dataground-release-device
    if head -c 1 /dev/zero >/dev/null 2>/dev/null; then
      exit 1
    fi
    printf "%s\n" strict-policy-ok
  ')
test "$output" = strict-policy-ok
environment=$("${cli[@]}" sandbox exec "$strict_sandbox" --no-tty --no-login-shell --timeout 20 -- /usr/bin/env)
if [[ "$environment" == *synthetic-account* || "$environment" == *synthetic-token* ]]; then
  echo "synthetic provider credentials were exposed to the strict workload" >&2
  exit 1
fi
unset environment
"${cli[@]}" sandbox delete "$strict_sandbox"

# A provider-free sandbox requires the explicit device and workload settings.
"${cli[@]}" sandbox create --name "$provider_free" --from "$workload" \
  --no-auto-providers --approval-mode manual --policy deploy/openshell/local/deny-all.yaml \
  --label dataground-release-test=true --no-tty --no-keep -- /usr/bin/true
"${cli[@]}" sandbox list --output json --selector dataground-release-test=true |
  jq -e '.sandboxes == [] and .next_page_token == ""'

DATAGROUND_TEST_OPENSHELL_RELEASE_ROOT="$PWD" \
  DATAGROUND_TEST_OPENSHELL_PROVIDER="$provider" \
  DATAGROUND_TEST_OPENSHELL_WORKLOAD="$workload" \
  go test ./internal/execution/openshell -run '^TestWorkspaceCLILiveLifecycle$' -count=1 -timeout=5m

"${cli[@]}" provider delete "$provider"
"${cli[@]}" provider list --output json | jq -e '.providers == [] and .next_page_token == ""'
test -z "$(docker ps --all --quiet --filter label=openshell.sandbox_namespace=dataground-local-v019)"
