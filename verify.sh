#!/usr/bin/env bash
# verify.sh - offline, reproducible verification gate for nbio.
#
# Stages (never touches the network, never starts external services):
#   gofmt       - all .go files must be gofmt-clean
#   vet         - go vet for every package
#   test        - unit tests for the host platform
#   test-race   - unit tests with the race detector for the host platform
#   build       - go build for every package (host platform)
#   exemptions  - validate verify-exemptions.json against real packages/targets
#   cross-build - compile (never execute) every package for all VERIFY_TARGETS
#
# Results are written to artifacts/verify-manifest.json in a stable order,
# without absolute paths or timestamps. Logs are kept in artifacts/logs/.
# Exit code is non-zero if any stage fails.

set -u -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

GO="${GO:-go}"
ARTIFACTS_DIR="artifacts"
LOG_DIR="$ARTIFACTS_DIR/logs"
MANIFEST="$ARTIFACTS_DIR/verify-manifest.json"
EXEMPTIONS_FILE="verify-exemptions.json"

# Cross-compilation targets (GOOS/GOARCH). Artifacts are never executed.
# Tests parse this line to make sure new platform-specific files are covered.
VERIFY_TARGETS="linux/amd64 darwin/amd64 freebsd/amd64 windows/amd64"

TEST_TIMEOUT="${VERIFY_TEST_TIMEOUT:-600}"
RACE_TIMEOUT="${VERIFY_RACE_TIMEOUT:-1800}"

# --- cleanup of our own previous artifacts ---------------------------------
rm -rf "$ARTIFACTS_DIR"
mkdir -p "$LOG_DIR"

# --- helpers ---------------------------------------------------------------

json_escape() {
	printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# json_str_array item... -> JSON array of strings on stdout
json_str_array() {
	local out="[" item first=1
	for item in "$@"; do
		if [ "$first" = "1" ]; then
			first=0
		else
			out="$out,"
		fi
		out="$out\"$(json_escape "$item")\""
	done
	out="$out]"
	printf '%s' "$out"
}

STAGE_JSONS=""
FAILED_STAGES=""

add_stage_json() {
	if [ -z "$STAGE_JSONS" ]; then
		STAGE_JSONS="$1"
	else
		STAGE_JSONS="$STAGE_JSONS,$1"
	fi
}

mark_failed() {
	FAILED_STAGES="$FAILED_STAGES $1"
}

# run_stage <name> <cmd...>: run cmd, log to artifacts/logs/<name>.log
run_stage() {
	local name="$1"
	shift
	echo "==> $name"
	if "$@" >"$LOG_DIR/$name.log" 2>&1; then
		echo "    pass"
		return 0
	fi
	echo "    FAIL (see $LOG_DIR/$name.log)"
	return 1
}

# --- environment facts ------------------------------------------------------

MODULE="$("$GO" list -m)"
GOVERSION="$("$GO" env GOVERSION)"
HOST_PLATFORM="$("$GO" env GOOS)/$("$GO" env GOARCH)"
ALL_PKGS="$("$GO" list ./...)"

# --- stage: gofmt -----------------------------------------------------------

GO_FILES="$(find . -name '*.go' -type f \
	-not -path './.git/*' -not -path "./$ARTIFACTS_DIR/*" | sort)"
# shellcheck disable=SC2086
UNFORMATTED="$(echo "$GO_FILES" | xargs gofmt -l | sed 's|^\./||' | sort)"
if [ -z "$UNFORMATTED" ]; then
	echo "==> gofmt"
	echo "    pass"
	gofmt_result="pass"
else
	echo "==> gofmt"
	echo "    FAIL (unformatted files listed in $LOG_DIR/gofmt.log)"
	printf '%s\n' "$UNFORMATTED" >"$LOG_DIR/gofmt.log"
	gofmt_result="fail"
	mark_failed gofmt
fi
# shellcheck disable=SC2086
add_stage_json "{\"name\":\"gofmt\",\"result\":\"$gofmt_result\",\"files\":$(json_str_array $UNFORMATTED)}"

# --- stage: vet -------------------------------------------------------------

# shellcheck disable=SC2086
if run_stage vet "$GO" vet $ALL_PKGS; then
	vet_result="pass"
else
	vet_result="fail"
	mark_failed vet
fi
# shellcheck disable=SC2086
add_stage_json "{\"name\":\"vet\",\"result\":\"$vet_result\",\"packages\":$(json_str_array $ALL_PKGS)}"

# --- stage: test ------------------------------------------------------------

# shellcheck disable=SC2086
if run_stage test "$GO" test -count=1 -timeout "${TEST_TIMEOUT}s" \
	-covermode=atomic -coverprofile="$ARTIFACTS_DIR/coverage.out" $ALL_PKGS; then
	test_result="pass"
else
	test_result="fail"
	mark_failed test
fi
# shellcheck disable=SC2086
add_stage_json "{\"name\":\"test\",\"result\":\"$test_result\",\"packages\":$(json_str_array $ALL_PKGS)}"

# --- stage: test-race -------------------------------------------------------

# shellcheck disable=SC2086
if run_stage test-race "$GO" test -race -count=1 -timeout "${RACE_TIMEOUT}s" $ALL_PKGS; then
	race_result="pass"
else
	race_result="fail"
	mark_failed test-race
fi
# shellcheck disable=SC2086
add_stage_json "{\"name\":\"test-race\",\"result\":\"$race_result\",\"packages\":$(json_str_array $ALL_PKGS)}"

# --- stage: build -----------------------------------------------------------

# shellcheck disable=SC2086
if run_stage build "$GO" build $ALL_PKGS; then
	build_result="pass"
else
	build_result="fail"
	mark_failed build
fi
# shellcheck disable=SC2086
add_stage_json "{\"name\":\"build\",\"result\":\"$build_result\",\"packages\":$(json_str_array $ALL_PKGS)}"

# --- stage: exemptions ------------------------------------------------------
# verify-exemptions.json lists packages that may be skipped for specific
# cross-compilation targets (e.g. cgo). Every entry must name a real package
# of this module and a target from VERIFY_TARGETS.

EXEMPTIONS_TSV=""
if run_stage exemptions "$GO" run ./tools/verifyexempt "$EXEMPTIONS_FILE"; then
	EXEMPTIONS_TSV="$(cat "$LOG_DIR/exemptions.log")"
	: >"$LOG_DIR/exemptions.log"
	exemptions_result="pass"
	while IFS="$(printf '\t')" read -r ex_target ex_pkg ex_reason; do
		[ -z "$ex_target" ] && continue
		case " $VERIFY_TARGETS " in
		*" $ex_target "*) ;;
		*)
			echo "exemption target '$ex_target' is not in VERIFY_TARGETS" >>"$LOG_DIR/exemptions.log"
			exemptions_result="fail"
			;;
		esac
		if ! printf '%s\n' "$ALL_PKGS" | grep -qxF "$ex_pkg"; then
			echo "exemption package '$ex_pkg' is not a package of this module" >>"$LOG_DIR/exemptions.log"
			exemptions_result="fail"
		fi
	done <<EOF_EXEMPTIONS
$EXEMPTIONS_TSV
EOF_EXEMPTIONS
else
	exemptions_result="fail"
fi
if [ "$exemptions_result" = "fail" ]; then
	mark_failed exemptions
	echo "    exemptions validation failed (see $LOG_DIR/exemptions.log)"
fi

exemptions_json="["
ex_first=1
while IFS="$(printf '\t')" read -r ex_target ex_pkg ex_reason; do
	[ -z "$ex_target" ] && continue
	if [ "$ex_first" = "1" ]; then
		ex_first=0
	else
		exemptions_json="$exemptions_json,"
	fi
	exemptions_json="$exemptions_json{\"target\":\"$ex_target\",\"package\":\"$ex_pkg\",\"reason\":\"$(json_escape "$ex_reason")\"}"
done <<EOF_EXEMPTIONS2
$EXEMPTIONS_TSV
EOF_EXEMPTIONS2
exemptions_json="$exemptions_json]"
add_stage_json "{\"name\":\"exemptions\",\"result\":\"$exemptions_result\",\"exemptions\":$exemptions_json}"

# --- stage: cross-build -----------------------------------------------------

cross_result="pass"
targets_json="["
target_first=1
for target in $VERIFY_TARGETS; do
	goos="${target%/*}"
	goarch="${target#*/}"

	target_pkgs="$(GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$GO" list ./...)"
	if [ -z "$target_pkgs" ]; then
		echo "    FAIL (no packages for $target)"
		cross_result="fail"
		continue
	fi

	# packages exempted for this target (explicit, verified above)
	exempted=""
	while IFS="$(printf '\t')" read -r ex_target ex_pkg ex_reason; do
		[ -z "$ex_target" ] && continue
		if [ "$ex_target" = "$target" ]; then
			exempted="$exempted $ex_pkg"
		fi
	done <<EOF_EXEMPTIONS3
$EXEMPTIONS_TSV
EOF_EXEMPTIONS3

	build_pkgs=""
	for pkg in $target_pkgs; do
		case " $exempted " in
		*" $pkg "*) ;;
		*) build_pkgs="$build_pkgs $pkg" ;;
		esac
	done

	log_name="cross-build-$(printf '%s' "$target" | tr '/' '-')"
	# compile only: building multiple packages discards all outputs and
	# never executes anything; no -o flag, because "go build -o dir/"
	# silently skips non-main packages
	# shellcheck disable=SC2086
	if run_stage "$log_name" env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
		"$GO" build $build_pkgs; then
		target_result="pass"
	else
		target_result="fail"
		cross_result="fail"
	fi

	# shellcheck disable=SC2086
	target_entry="{\"target\":\"$target\",\"result\":\"$target_result\",\"packages\":$(json_str_array $build_pkgs),\"exempted\":$(json_str_array $exempted)}"
	if [ "$target_first" = "1" ]; then
		target_first=0
	else
		targets_json="$targets_json,"
	fi
	targets_json="$targets_json$target_entry"
done
targets_json="$targets_json]"
if [ "$cross_result" = "fail" ]; then
	mark_failed cross-build
fi
add_stage_json "{\"name\":\"cross-build\",\"result\":\"$cross_result\",\"targets\":$targets_json}"

# --- manifest ---------------------------------------------------------------

if [ -n "$FAILED_STAGES" ]; then
	overall_result="fail"
else
	overall_result="pass"
fi

{
	printf '{\n'
	printf '  "schemaVersion": 1,\n'
	printf '  "module": "%s",\n' "$MODULE"
	printf '  "goVersion": "%s",\n' "$GOVERSION"
	printf '  "platform": "%s",\n' "$HOST_PLATFORM"
	printf '  "stages": [%s],\n' "$STAGE_JSONS"
	printf '  "result": "%s"\n' "$overall_result"
	printf '}\n'
} >"$MANIFEST"

echo
echo "manifest: $MANIFEST"
if [ "$overall_result" = "pass" ]; then
	echo "verify: PASS"
	exit 0
fi
echo "verify: FAIL (failed stages:$FAILED_STAGES)"
exit 1
