#!/bin/sh
# End-to-end check of the CLI against a platform running on this machine.
#
#   TALKSASA_E2E_URL=http://127.0.0.1:8011 \
#   TALKSASA_E2E_TOKEN=... \          # read, deploy, env
#   TALKSASA_E2E_READ_TOKEN=... \     # read only
#   TALKSASA_E2E_APP=19 \
#   TALKSASA_PLATFORM_DIR=../talksasa-cloud \   # optional: compare error codes
#   scripts/e2e.sh
#
# It changes nothing an app runs on: environment writes use --no-apply and
# are removed again, and nothing is deployed. It refuses any address that is
# not on this machine, so it can never be pointed at production.
set -eu

: "${TALKSASA_E2E_URL:?set TALKSASA_E2E_URL}"
: "${TALKSASA_E2E_TOKEN:?set TALKSASA_E2E_TOKEN}"
: "${TALKSASA_E2E_READ_TOKEN:?set TALKSASA_E2E_READ_TOKEN}"
: "${TALKSASA_E2E_APP:?set TALKSASA_E2E_APP}"

case "$TALKSASA_E2E_URL" in
  http://127.0.0.1:* | http://localhost:* | http://\[::1\]:*) ;;
  *) echo "refusing $TALKSASA_E2E_URL: the e2e check only runs against this machine" >&2; exit 2 ;;
esac

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

go build -o "$work/talksasa" "$root"
T="$work/talksasa"

export TALKSASA_CONFIG_DIR="$work/config" TALKSASA_NO_KEYRING=1 TALKSASA_API_URL="$TALKSASA_E2E_URL"
unset TALKSASA_TOKEN TALKSASA_APP || true

passed=0
failed=0
# expect <exit code> <description> <command...>
expect() {
  want=$1 desc=$2
  shift 2
  set +e
  "$@" >"$work/out" 2>"$work/err"
  got=$?
  set -e
  if [ "$got" = "$want" ]; then
    passed=$((passed + 1))
    echo "ok    $desc"
  else
    failed=$((failed + 1))
    echo "FAIL  $desc: exit $got, want $want" >&2
    sed 's/^/      /' "$work/out" "$work/err" >&2
  fi
}

# expect_out <text> <description>: the last command's output contains text.
expect_out() {
  if grep -qF -- "$1" "$work/out" "$work/err"; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    echo "FAIL  $2: output lacks \"$1\"" >&2
  fi
}

cd "$work"

printf '%s\n' "$TALKSASA_E2E_TOKEN" >"$work/token"
expect 0 "login" sh -c "\"$T\" login < \"$work/token\""
expect 0 "whoami" "$T" whoami
expect 0 "apps" "$T" apps
expect 0 "link" "$T" link "$TALKSASA_E2E_APP"
expect 0 "status" "$T" status
expect 0 "status --json is one document" sh -c "\"$T\" status --json | python3 -c 'import json,sys; json.load(sys.stdin)'"
expect 0 "env list" "$T" env list
expect_out "HAS VALUE" "env list shows whether a value is set, not the value"
expect 0 "env set from hidden input, not applied" sh -c "printf 'one\ntwo' | \"$T\" env set CLI_E2E_SECRET --no-apply"
expect 0 "env unset, not applied" "$T" env unset CLI_E2E_SECRET --no-apply
expect 7 "a platform-managed variable is refused" "$T" env set DB_PASSWORD=x --no-apply
expect 0 "domains" "$T" domains
expect 0 "metrics" "$T" metrics
expect 0 "backups" "$T" backups
expect 0 "deploy status" "$T" deploy status
expect 5 "a backup that is not there" "$T" backups restore 999999999 --yes
expect 2 "stop asks first" "$T" stop
expect 4 "another customer's app" "$T" status --app 1

expect 4 "a read token cannot redeploy" env TALKSASA_TOKEN="$TALKSASA_E2E_READ_TOKEN" "$T" redeploy --no-wait
expect_out '"deploy" permission' "the refusal names the permission"
expect 4 "a read token cannot set variables" env TALKSASA_TOKEN="$TALKSASA_E2E_READ_TOKEN" "$T" env set A=1 --no-apply
expect 3 "a wrong token" env TALKSASA_TOKEN="1|tsk_wrong" "$T" apps

expect 0 "logout" "$T" logout
expect 3 "signed out" "$T" whoami

# The CLI's exit-code map covers exactly the platform's documented codes.
if [ -n "${TALKSASA_PLATFORM_DIR:-}" ]; then
  (cd "$TALKSASA_PLATFORM_DIR" && php artisan tinker --execute \
    'echo json_encode(array_keys(App\Support\Api\ApiError::documentedCodes()), JSON_PRETTY_PRINT);') |
    tail -n +1 >"$work/codes.json"
  if python3 -c "import json,sys; a=json.load(open(sys.argv[1])); b=json.load(open(sys.argv[2])); sys.exit(0 if sorted(a)==sorted(b) else 1)" \
    "$work/codes.json" "$root/internal/cli/testdata/api_error_codes.json"; then
    passed=$((passed + 1)); echo "ok    error codes match the platform"
  else
    failed=$((failed + 1))
    echo "FAIL  internal/cli/testdata/api_error_codes.json differs from the platform's ApiError::documentedCodes()" >&2
  fi
fi

echo "$passed passed, $failed failed"
[ "$failed" = 0 ]
