#!/usr/bin/env bash
# verify.sh - offline, reproducible verification gate for nbio.
#
# Stages (fixed order):
#   fmt        gofmt -l over all source files
#   vet        go vet ./...
#   build      go build ./... (all packages, including commands)
#   test       go test for the host platform
#   test-race  go test -race for the host platform
#   cross      CGO_ENABLED=0 go build for every VERIFY_CROSS_TARGETS entry
#
# The gate never accesses the network and never starts external services.
# Cross-compilation only compiles; produced artifacts are never executed.
# Packages that cannot be cross-compiled (e.g. cgo) must be listed with a
# reason in verify/cross_exemptions.txt; failures are never swallowed.
#
# Results are written to artifacts/verify-manifest.json (deterministic:
# stable ordering, no absolute paths, no timestamps) plus per-stage logs
# under artifacts/logs/. Logs are kept on failure and the exit code is
# non-zero if any stage fails.
#
# Environment knobs:
#   GO                    go binary to use (default: go)
#   VERIFY_STAGES         stages to run, space separated (default: all)
#   VERIFY_ARTIFACTS_DIR  output directory (default: artifacts,
#                         relative paths resolve against the repo root)
#   VERIFY_TEST_TIMEOUT   per-package timeout for go test (default: 30m)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

GO="${GO:-go}"
VERIFY_STAGES="${VERIFY_STAGES:-fmt vet build test test-race cross}"
VERIFY_TEST_TIMEOUT="${VERIFY_TEST_TIMEOUT:-30m}"
ARTIFACTS_DIR="${VERIFY_ARTIFACTS_DIR:-artifacts}"
case "$ARTIFACTS_DIR" in
	/*) ;;
	*) ARTIFACTS_DIR="$REPO_ROOT/$ARTIFACTS_DIR" ;;
esac
LOG_DIR="$ARTIFACTS_DIR/logs"
MANIFEST="$ARTIFACTS_DIR/verify-manifest.json"

# Cross-compilation targets as GOOS/GOARCH, space separated.
# verify/verify_test.go parses this exact line; keep the format stable.
VERIFY_CROSS_TARGETS="linux/amd64 darwin/amd64 freebsd/amd64 windows/amd64"

EXEMPTIONS_FILE="verify/cross_exemptions.txt"

if [ -z "$ARTIFACTS_DIR" ] || [ "$ARTIFACTS_DIR" = "/" ]; then
	echo "verify.sh: refusing unsafe artifacts dir '$ARTIFACTS_DIR'" >&2
	exit 2
fi

# Clean our own previous outputs before running.
rm -rf "$LOG_DIR" "$MANIFEST"
mkdir -p "$LOG_DIR"

GO_BIN="$(command -v "$GO" || true)"
if [ -z "$GO_BIN" ]; then
	echo "verify.sh: go binary '$GO' not found in PATH" >&2
	exit 2
fi
GOFMT="$(dirname "$GO_BIN")/gofmt"
[ -x "$GOFMT" ] || GOFMT="gofmt"

stage_enabled() {
	case " $VERIFY_STAGES " in
	*" $1 "*) return 0 ;;
	*) return 1 ;;
	esac
}

# read_exemptions <goos/goarch>: print exempted packages for a target.
read_exemptions() {
	[ -f "$EXEMPTIONS_FILE" ] || return 0
	local target pkg reason
	while read -r target pkg reason; do
		case "$target" in
		'' | \#*) continue ;;
		esac
		if [ "$target" = "$1" ]; then
			printf '%s\n' "$pkg"
		fi
	done <"$EXEMPTIONS_FILE"
}

# json_array <indent>: read newline-separated items on stdin, print a JSON array.
json_array() {
	local indent="$1" first=1 item
	printf '['
	while IFS= read -r item; do
		[ -z "$item" ] && continue
		if [ "$first" -eq 1 ]; then
			first=0
		else
			printf ','
		fi
		printf '\n%s"%s"' "$indent" "$item"
	done
	if [ "$first" -eq 0 ]; then
		printf '\n%s' "${indent#??}"
	fi
	printf ']'
}

GO_VERSION="$("$GO" version | awk '{print $3}')"
HOST_GOOS="$("$GO" env GOOS)"
HOST_GOARCH="$("$GO" env GOARCH)"
MODULE="$("$GO" list -m)"
PACKAGES="$("$GO" list ./... | LC_ALL=C sort)"

FAILED=0
STAGE_STATUS=()
TARGET_RESULTS=()

note_failure() {
	local name="$1" log="$2"
	FAILED=1
	echo "verify.sh: stage '$name' failed, see $log" >&2
	tail -n 40 "$log" >&2
}

run_stage() {
	local name="$1"
	shift
	local log="$LOG_DIR/$name.log"
	local status
	if [ "$FAILED" -ne 0 ] || ! stage_enabled "$name"; then
		echo "skipped" >"$log"
		status="skipped"
	elif "$@" >"$log" 2>&1; then
		status="passed"
	else
		status="failed"
		note_failure "$name" "$log"
	fi
	STAGE_STATUS+=("$name:$status")
}

stage_fmt() {
	local files unformatted
	files="$(find . -path ./.git -prune -o -type f -name '*.go' -print | LC_ALL=C sort)"
	unformatted="$(printf '%s\n' "$files" | xargs "$GOFMT" -l)"
	if [ -n "$unformatted" ]; then
		echo "gofmt: files need formatting:"
		printf '%s\n' "$unformatted"
		return 1
	fi
	echo "gofmt: all files formatted"
}

stage_vet() {
	"$GO" vet ./...
}

stage_build() {
	"$GO" build ./...
}

stage_test() {
	"$GO" test -count=1 -timeout "$VERIFY_TEST_TIMEOUT" ./...
}

stage_test_race() {
	"$GO" test -race -count=1 -timeout "$VERIFY_TEST_TIMEOUT" ./...
}

# target_packages <goos/goarch>: print the package set built for a target.
target_packages() {
	local exempt
	exempt="$(read_exemptions "$1" | LC_ALL=C sort)"
	if [ -n "$exempt" ]; then
		comm -23 <(printf '%s\n' "$PACKAGES") <(printf '%s\n' "$exempt")
	else
		printf '%s\n' "$PACKAGES"
	fi
}

stage_cross() {
	local target goos goarch build_pkgs log tstatus
	: >"$LOG_DIR/cross.log"
	for target in $VERIFY_CROSS_TARGETS; do
		goos="${target%/*}"
		goarch="${target#*/}"
		log="$LOG_DIR/cross-$goos-$goarch.log"
		build_pkgs="$(target_packages "$target")"
		tstatus="passed"
		if [ -n "$build_pkgs" ]; then
			# Compile only; cross-compiled artifacts are never executed.
			if ! GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$GO" build $build_pkgs >"$log" 2>&1; then
				tstatus="failed"
				note_failure "cross/$target" "$log"
			fi
		else
			echo "no packages to build for $target" >"$log"
		fi
		echo "$target: $tstatus" >>"$LOG_DIR/cross.log"
		TARGET_RESULTS+=("$target|$goos|$goarch|$tstatus")
	done
}

run_stage fmt stage_fmt
run_stage vet stage_vet
run_stage build stage_build
run_stage test stage_test
run_stage test-race stage_test_race
if [ "$FAILED" -eq 0 ] && stage_enabled cross; then
	stage_cross
	if [ "$FAILED" -eq 0 ]; then
		STAGE_STATUS+=("cross:passed")
	else
		STAGE_STATUS+=("cross:failed")
	fi
else
	echo "skipped" >"$LOG_DIR/cross.log"
	STAGE_STATUS+=("cross:skipped")
	for target in $VERIFY_CROSS_TARGETS; do
		TARGET_RESULTS+=("$target|${target%/*}|${target#*/}|skipped")
	done
fi

RESULT="passed"
[ "$FAILED" -ne 0 ] && RESULT="failed"

write_manifest() {
	local rel_logs="logs"
	local i entry name status target goos goarch tstatus exempt pkgs
	{
		printf '{\n'
		printf '  "schemaVersion": 1,\n'
		printf '  "goVersion": "%s",\n' "$GO_VERSION"
		printf '  "module": "%s",\n' "$MODULE"
		printf '  "platform": {\n'
		printf '    "goos": "%s",\n' "$HOST_GOOS"
		printf '    "goarch": "%s"\n' "$HOST_GOARCH"
		printf '  },\n'
		printf '  "packages": '
		printf '%s\n' "$PACKAGES" | json_array '    '
		printf ',\n'
		printf '  "stages": [\n'
		for i in "${!STAGE_STATUS[@]}"; do
			entry="${STAGE_STATUS[$i]}"
			name="${entry%%:*}"
			status="${entry#*:}"
			printf '    {\n'
			printf '      "name": "%s",\n' "$name"
			printf '      "status": "%s",\n' "$status"
			printf '      "log": "%s/%s.log"\n' "$rel_logs" "$name"
			if [ "$i" -lt $((${#STAGE_STATUS[@]} - 1)) ]; then
				printf '    },\n'
			else
				printf '    }\n'
			fi
		done
		printf '  ],\n'
		printf '  "crossTargets": [\n'
		for i in "${!TARGET_RESULTS[@]}"; do
			entry="${TARGET_RESULTS[$i]}"
			target="$(printf '%s' "$entry" | cut -d'|' -f1)"
			goos="$(printf '%s' "$entry" | cut -d'|' -f2)"
			goarch="$(printf '%s' "$entry" | cut -d'|' -f3)"
			tstatus="$(printf '%s' "$entry" | cut -d'|' -f4)"
			if [ "$tstatus" = "skipped" ]; then
				pkgs=""
				exempt=""
			else
				exempt="$(read_exemptions "$target" | LC_ALL=C sort)"
				pkgs="$(target_packages "$target")"
			fi
			printf '    {\n'
			printf '      "target": "%s",\n' "$target"
			printf '      "goos": "%s",\n' "$goos"
			printf '      "goarch": "%s",\n' "$goarch"
			printf '      "status": "%s",\n' "$tstatus"
			printf '      "log": "%s/cross-%s-%s.log",\n' "$rel_logs" "$goos" "$goarch"
			printf '      "packages": '
			printf '%s\n' "$pkgs" | json_array '        '
			printf ',\n'
			printf '      "exemptions": '
			printf '%s\n' "$exempt" | json_array '        '
			printf '\n'
			if [ "$i" -lt $((${#TARGET_RESULTS[@]} - 1)) ]; then
				printf '    },\n'
			else
				printf '    }\n'
			fi
		done
		printf '  ],\n'
		printf '  "result": "%s"\n' "$RESULT"
		printf '}\n'
	} >"$MANIFEST"
}

write_manifest

echo "verify.sh: $RESULT (manifest: $MANIFEST)"
[ "$FAILED" -eq 0 ]
