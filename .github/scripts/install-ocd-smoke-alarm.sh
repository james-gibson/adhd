#!/usr/bin/env bash
# Install the ocd-smoke-alarm binary that the tests/scenario interop suite
# spawns, and prove it runs before the suite is allowed to skip.
#
# The interop suite is the contract check between adhd and smoke-alarm: without
# it, a release can ship a wire-format regression that nothing noticed.  It
# skips itself when the binary is missing, so a workflow that forgets this step
# still goes green.  Hence the ordering below — install, wire the path, then
# probe the effect and refuse to continue if the binary is not usable.  A
# warning here would be shippable; a non-zero exit is not.
#
# The pinned version is the single source of truth for CI and the release
# workflow.  They must exercise the same smoke-alarm contract, so a tag never
# validates against a different binary than main did.  Bump it here and both
# workflows move together.

set -euo pipefail

# Keep in sync with nothing else: this is the only place the version is named.
OCD_SMOKE_ALARM_VERSION="${OCD_SMOKE_ALARM_VERSION:-v0.11.0}"
SMOKE_ALARM_PKG="github.com/james-gibson/smoke-alarm/cmd/ocd-smoke-alarm"

fail() {
  printf 'install-ocd-smoke-alarm: %s\n' "$*" >&2
  exit 1
}

# Where `go install` puts binaries. GOBIN wins when set, else $GOPATH/bin.
install_dir="$(go env GOBIN)"
if [ -z "$install_dir" ]; then
  install_dir="$(go env GOPATH)/bin"
fi

printf 'install-ocd-smoke-alarm: installing %s@%s\n' "$SMOKE_ALARM_PKG" "$OCD_SMOKE_ALARM_VERSION"
go install "${SMOKE_ALARM_PKG}@${OCD_SMOKE_ALARM_VERSION}" ||
  fail "go install ${SMOKE_ALARM_PKG}@${OCD_SMOKE_ALARM_VERSION} failed; the interop suite cannot run"

# go install writes to $install_dir, which is not on PATH by default. Export it
# for this step so the probe below resolves what we just built, and append it to
# GITHUB_PATH so the test step (a different process) finds it too.
export PATH="$install_dir:$PATH"
if [ -n "${GITHUB_PATH:-}" ]; then
  printf '%s\n' "$install_dir" >>"$GITHUB_PATH"
fi

resolved="${install_dir}/ocd-smoke-alarm"
[ -x "$resolved" ] ||
  fail "expected an executable at $resolved after install; refusing to run a suite that would skip"

# Probe the effect, not the intent: run the binary and confirm it is the pinned
# build. A stale ocd-smoke-alarm earlier on PATH would otherwise satisfy the
# tests while quietly testing a different contract than the one pinned here.
reported=""
status=0
reported="$("$resolved" --version 2>&1)" || status=$?
case "$status" in
0) ;;
126 | 127)
  fail "$resolved exists but cannot be executed (exit $status)"
  ;;
*)
  # It ran and rejected the flag. That still proves the loader works; the
  # identity check below is what matters.
  ;;
esac

case "$reported" in
*"$OCD_SMOKE_ALARM_VERSION"*) ;;
*) fail "$resolved reported '$reported', expected version $OCD_SMOKE_ALARM_VERSION" ;;
esac

printf 'install-ocd-smoke-alarm: ready — %s (%s)\n' "$resolved" "$reported"

# Hand the exact path to the test step so the interop suite spawns this binary
# and not whichever ocd-smoke-alarm PATH resolution happens to find.
if [ -n "${GITHUB_ENV:-}" ]; then
  printf 'OCD_SMOKE_ALARM_BIN=%s\n' "$resolved" >>"$GITHUB_ENV"
fi
