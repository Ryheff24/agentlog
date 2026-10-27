#!/bin/sh
# Statement coverage per package, counted across the whole test suite: a
# package's code run by another package's tests counts as covered.
#
#   scripts/coverage.sh           print coverage
#   scripts/coverage.sh --update  record it as the baseline in coverage.txt
#   scripts/coverage.sh --check   fail if the total or any package fell below
#                                 the baseline
#
# The baseline only moves up by hand, with --update, after the new numbers
# have been looked at.
set -eu
cd "$(dirname "$0")/.."
profile=$(mktemp "${TMPDIR:-/tmp}/agentlog-cover.XXXXXX")
trap 'rm -f "$profile"' EXIT
# Only packages with tests are run; the rest are still measured through
# -coverpkg. (Running a package without tests under -coverpkg needs the
# covdata tool, which some Go installs leave out.)
tested=$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
# shellcheck disable=SC2086
go test -count=1 -coverpkg=./... -coverprofile="$profile" $tested >/dev/null

# A block is covered if any test binary ran it.
table=$(awk -F'[: ]' 'NR > 1 {
	key = $1 ":" $2
	stmts[key] = $3
	if ($4 > 0) hit[key] = 1
	pkg = $1; sub(/\/[^\/]*$/, "", pkg); sub(/^github.com\/Ryheff24\/agentlog\//, "", pkg)
	owner[key] = pkg
}
END {
	for (k in stmts) {
		p = owner[k]; total[p] += stmts[k]; all += stmts[k]
		if (k in hit) { covered[p] += stmts[k]; allc += stmts[k] }
	}
	for (p in total) printf "%-22s %5.1f%%  %5d statements\n", p, 100 * covered[p] / total[p], total[p]
	printf "%-22s %5.1f%%  %5d statements\n", "TOTAL", 100 * allc / all, all
}' "$profile" | sort)

case "${1:-}" in
--update)
	printf '%s\n' "$table" > coverage.txt
	printf '%s\n' "$table"
	;;
--check)
	printf '%s\n' "$table" | awk 'NR == FNR { base[$1] = $2 + 0; next }
	($1 in base) && $2 + 0 < base[$1] - 0.05 {
		printf "coverage fell: %s %s, baseline %.1f%%\n", $1, $2, base[$1]; bad = 1
	}
	END { exit bad }' coverage.txt -
	;;
*)
	printf '%s\n' "$table"
	;;
esac
