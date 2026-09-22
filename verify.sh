#!/usr/bin/env bash
#
# verify.sh - reproducible, offline verification gate for nbio.
#
# Stages (default: all, in this order):
#   gofmt        check formatting of all Go sources
#   vet          go vet all packages
#   build        compile all packages (including examples, if any)
#   test         run unit tests on the current platform
#   test-race    run unit tests with the race detector
#   cross-build  compile + vet every package for linux/darwin/freebsd/windows
#
# Usage:
#   ./verify.sh                 run all stages
#   ./verify.sh gofmt vet ...   run only the listed stages
#   ./verify.sh --list-targets  print cross-build targets, one per line
#
# The script never accesses the network and never starts external
# services. Cross-compilation artifacts are never executed. Packages
# that cannot be cross-compiled for a target (e.g. cgo) must be listed
# explicitly in verify-exemptions.json; failures are never swallowed.
#
# Results are written to artifacts/verify-manifest.json (stable key
# order, no absolute paths, no timestamps). Stage logs are kept under
# artifacts/logs/ on failure.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

ARTIFACTS_DIR="artifacts"
LOG_DIR="$ARTIFACTS_DIR/logs"
MANIFEST="$ARTIFACTS_DIR/verify-manifest.json"
EXEMPTIONS_FILE="verify-exemptions.json"

ALL_STAGES=(gofmt vet build test test-race cross-build)
CROSS_TARGETS=(linux darwin freebsd windows)
CROSS_GOARCH=amd64
TEST_TIMEOUT=900s

GO=go

usage() {
	sed -n '2,20p' "$ROOT/verify.sh" | sed 's/^# \{0,1\}//'
}

if [ "${1:-}" = "--list-targets" ]; then
	printf '%s\n' "${CROSS_TARGETS[@]}"
	exit 0
fi

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
	usage
	exit 0
fi

if [ "$#" -gt 0 ]; then
	STAGES=("$@")
else
	STAGES=("${ALL_STAGES[@]}")
fi

for stage in "${STAGES[@]}"; do
	valid=0
	for known in "${ALL_STAGES[@]}"; do
		if [ "$stage" = "$known" ]; then
			valid=1
			break
		fi
	done
	if [ "$valid" -ne 1 ]; then
		echo "verify: unknown stage '$stage'" >&2
		usage >&2
		exit 2
	fi
done

# --- helpers ---------------------------------------------------------------

# json_array reads newline-separated items on stdin and prints a JSON array.
json_array() {
	local out="" line
	while IFS= read -r line; do
		[ -z "$line" ] && continue
		line="${line//\\/\\\\}"
		line="${line//\"/\\\"}"
		out="${out:+$out,}\"$line\""
	done
	printf '[%s]' "$out"
}

# list_packages prints the module's packages, sorted for stable output.
list_packages() {
	"$GO" list ./... | sort
}

# exemptions_for GOOS prints exempted packages for the given target.
exemptions_for() {
	local goos="$1"
	[ -f "$EXEMPTIONS_FILE" ] || return 0
	grep -o "\"goos\"[^}]*\"package\"[^}]*" "$EXEMPTIONS_FILE" |
		sed -n "s/.*\"goos\"[[:space:]]*:[[:space:]]*\"$goos\".*\"package\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p"
}

is_exempt() {
	local goos="$1" pkg="$2" exempt
	while IFS= read -r exempt; do
		[ "$exempt" = "$pkg" ] && return 0
	done < <(exemptions_for "$goos")
	return 1
}

# --- stage bookkeeping -----------------------------------------------------

STAGE_RESULTS=()   # one JSON object per executed stage, in execution order
OVERALL_STATUS="passed"
FAILED_STAGE=""

# record_stage NAME STATUS EXTRA_JSON
record_stage() {
	local name="$1" status="$2" extra="${3:-}"
	if [ -n "$extra" ]; then
		extra=",$extra"
	fi
	STAGE_RESULTS+=("{\"name\":\"$name\",\"status\":\"$status\"$extra}")
}

# run_stage NAME LOGFILE CMD...  -> returns non-zero on failure (captured)
run_stage() {
	local name="$1" logfile="$2"
	shift 2
	echo "==> $name"
	if "$@" >"$logfile" 2>&1; then
		return 0
	fi
	echo "    failed, see $logfile" >&2
	return 1
}

# --- stages ----------------------------------------------------------------

stage_gofmt() {
	local log="$LOG_DIR/gofmt.log"
	echo "==> gofmt"
	gofmt -l . >"$log" 2>&1
	if [ -s "$log" ]; then
		echo "    failed, see $log" >&2
		record_stage gofmt failed "\"log\":\"$log\""
		return 1
	fi
	record_stage gofmt passed
	return 0
}

stage_vet() {
	local log="$LOG_DIR/vet.log"
	local pkgs
	pkgs="$(list_packages)"
	if ! run_stage vet "$log" "$GO" vet ./...; then
		record_stage vet failed "\"log\":\"$log\",\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
		return 1
	fi
	record_stage vet passed "\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
	return 0
}

stage_build() {
	local log="$LOG_DIR/build.log"
	local pkgs
	pkgs="$(list_packages)"
	if ! run_stage build "$log" "$GO" build ./...; then
		record_stage build failed "\"log\":\"$log\",\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
		return 1
	fi
	record_stage build passed "\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
	return 0
}

stage_test() {
	local log="$LOG_DIR/test.log"
	local pkgs
	pkgs="$(list_packages)"
	# -p 1: packages run sequentially so tests binding fixed loopback
	# ports in different packages cannot race each other.
	if ! run_stage test "$log" "$GO" test -count=1 -p 1 -timeout "$TEST_TIMEOUT" \
		-covermode=atomic -coverprofile="$LOG_DIR/coverage.out" ./...; then
		record_stage test failed "\"log\":\"$log\",\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
		return 1
	fi
	record_stage test passed "\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
	return 0
}

race_supported() {
	local probe
	probe="$(mktemp -d)"
	printf 'package main\nfunc main() {}\n' >"$probe/main.go"
	local rc=0
	(cd "$probe" && "$GO" build -race -o probe_bin main.go) >/dev/null 2>&1 || rc=1
	rm -rf "$probe"
	return "$rc"
}

stage_test_race() {
	local log="$LOG_DIR/test-race.log"
	local pkgs
	pkgs="$(list_packages)"
	if ! race_supported; then
		echo "==> test-race (skipped: race detector not supported on this platform)"
		record_stage test-race skipped "\"reason\":\"race detector not supported on this platform\""
		return 0
	fi
	if ! run_stage test-race "$log" "$GO" test -count=1 -p 1 -race -timeout "$TEST_TIMEOUT" ./...; then
		record_stage test-race failed "\"log\":\"$log\",\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
		return 1
	fi
	record_stage test-race passed "\"packages\":$(printf '%s\n' "$pkgs" | json_array)"
	return 0
}

# cross_build_target GOOS -> prints a JSON object for the target.
# Returns non-zero if any non-exempt package fails.
cross_build_target() {
	local goos="$1"
	local log="$LOG_DIR/cross-build-$goos.log"
	local pkgs_json status="passed" extra=""

	echo "==> cross-build $goos/$CROSS_GOARCH" >&2
	if GOOS="$goos" GOARCH="$CROSS_GOARCH" CGO_ENABLED=0 "$GO" build ./... >"$log" 2>&1 &&
		GOOS="$goos" GOARCH="$CROSS_GOARCH" CGO_ENABLED=0 "$GO" vet ./... >>"$log" 2>&1; then
		pkgs_json="$(GOOS="$goos" GOARCH="$CROSS_GOARCH" "$GO" list ./... | sort | json_array)"
		printf '{"goos":"%s","goarch":"%s","status":"passed","packages":%s,"exempt":[]}' \
			"$goos" "$CROSS_GOARCH" "$pkgs_json"
		return 0
	fi

	# Isolate failures per package; only explicitly exempted packages
	# (verify-exemptions.json) may be skipped.
	local pkg failed=() exempt=() built=()
	while IFS= read -r pkg; do
		if GOOS="$goos" GOARCH="$CROSS_GOARCH" CGO_ENABLED=0 "$GO" build "$pkg" >>"$log" 2>&1 &&
			GOOS="$goos" GOARCH="$CROSS_GOARCH" CGO_ENABLED=0 "$GO" vet "$pkg" >>"$log" 2>&1; then
			built+=("$pkg")
		elif is_exempt "$goos" "$pkg"; then
			exempt+=("$pkg")
		else
			failed+=("$pkg")
		fi
	done < <(GOOS="$goos" GOARCH="$CROSS_GOARCH" "$GO" list ./... | sort)

	if [ "${#failed[@]}" -gt 0 ]; then
		status="failed"
		OVERALL_STATUS="failed"
		FAILED_STAGE="cross-build"
		echo "    failed for $goos: ${failed[*]} (see $log)" >&2
	fi

	local built_json exempt_json
	if [ "${#built[@]}" -gt 0 ]; then
		built_json="$(printf '%s\n' "${built[@]}" | json_array)"
	else
		built_json="[]"
	fi
	if [ "${#exempt[@]}" -gt 0 ]; then
		exempt_json="$(printf '%s\n' "${exempt[@]}" | json_array)"
	else
		exempt_json="[]"
	fi
	printf '{"goos":"%s","goarch":"%s","status":"%s","packages":%s,"exempt":%s,"log":"%s"}' \
		"$goos" "$CROSS_GOARCH" "$status" "$built_json" "$exempt_json" "$log"
	[ "$status" = "passed" ]
}

stage_cross_build() {
	local target_json=() goos
	local rc=0
	for goos in "${CROSS_TARGETS[@]}"; do
		local result
		result="$(cross_build_target "$goos")" || rc=1
		target_json+=("$result")
	done
	local targets
	targets="$(printf '%s\n' "${target_json[@]}" | paste -sd, -)"
	if [ "$rc" -ne 0 ]; then
		record_stage cross-build failed "\"targets\":[$targets]"
		return 1
	fi
	record_stage cross-build passed "\"targets\":[$targets]"
	return 0
}

# --- run -------------------------------------------------------------------

# Clean our own previous artifacts before doing any work.
rm -rf "$ARTIFACTS_DIR"
mkdir -p "$LOG_DIR"

for stage in "${STAGES[@]}"; do
	case "$stage" in
	gofmt)       stage_gofmt       || { OVERALL_STATUS="failed"; FAILED_STAGE="$stage"; } ;;
	vet)         stage_vet         || { OVERALL_STATUS="failed"; FAILED_STAGE="$stage"; } ;;
	build)       stage_build       || { OVERALL_STATUS="failed"; FAILED_STAGE="$stage"; } ;;
	test)        stage_test        || { OVERALL_STATUS="failed"; FAILED_STAGE="$stage"; } ;;
	test-race)   stage_test_race   || { OVERALL_STATUS="failed"; FAILED_STAGE="$stage"; } ;;
	cross-build) stage_cross_build || { OVERALL_STATUS="failed"; FAILED_STAGE="$stage"; } ;;
	esac
	if [ "$OVERALL_STATUS" = "failed" ]; then
		break
	fi
done

# --- manifest --------------------------------------------------------------

GO_VERSION="$("$GO" version | awk '{print $3}')"
HOST_GOOS="$("$GO" env GOOS)"
HOST_GOARCH="$("$GO" env GOARCH)"

stages_json="$(printf '%s\n' "${STAGE_RESULTS[@]}" | paste -sd, -)"

{
	printf '{\n'
	printf '  "version": 1,\n'
	printf '  "goVersion": "%s",\n' "$GO_VERSION"
	printf '  "host": {"goos": "%s", "goarch": "%s"},\n' "$HOST_GOOS" "$HOST_GOARCH"
	printf '  "stages": [%s],\n' "$stages_json"
	printf '  "status": "%s"\n' "$OVERALL_STATUS"
	printf '}\n'
} >"$MANIFEST.tmp"
mv "$MANIFEST.tmp" "$MANIFEST"

echo
echo "verify: $OVERALL_STATUS (manifest: $MANIFEST)"

if [ "$OVERALL_STATUS" != "passed" ]; then
	echo "verify: stage '$FAILED_STAGE' failed; logs kept under $LOG_DIR" >&2
	exit 1
fi
exit 0
