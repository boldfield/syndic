#!/usr/bin/env bash
set -euo pipefail

# Check that a pull request leaves the design documents alone. This enforces
# the Process clause in the v5 delta, as read by OQ-6 in
# docs/design/open-questions.md. A pull request passes when either:
#   1. it changes no file under docs/design/ other than open-questions.md; or
#   2. it is a ratification: every file it changes is under docs/design/, and
#      every one of those other than open-questions.md is newly added. It
#      modifies, deletes and renames no existing design document and changes
#      nothing outside docs/design/.
# Every other pull request that touches docs/design/ fails.

# Determine the base ref for the comparison
if [[ -n "${GITHUB_BASE_REF:-}" ]]; then
	# In a GitHub pull request, use the base branch
	base_ref="origin/${GITHUB_BASE_REF}"
elif [[ -n "${GITHUB_REF:-}" ]] && [[ "${GITHUB_REF}" == "refs/heads/main" ]]; then
	# On main branch, skip the check - only PRs need to be blocked
	exit 0
else
	# Default fallback for local testing
	base_ref="origin/main"
fi

# Verify the base ref exists and is resolvable
if ! git rev-parse --verify "${base_ref}^{commit}" >/dev/null 2>&1; then
	echo "ERROR: Cannot resolve base ref ${base_ref}"
	exit 1
fi

# Read the diff NUL-delimited so paths are never quoted, and into a file so a
# git failure is not hidden (failing closed rather than open).
diff_file=$(mktemp)
trap 'rm -f "$diff_file"' EXIT
if ! git diff -z --name-status --no-renames "${base_ref}...HEAD" >"$diff_file"; then
	echo "ERROR: git diff failed against ${base_ref}"
	exit 1
fi

offending=()      # "<status> <path>" for design documents that may not change
added_design=()   # "A <path>" for newly added design documents
outside_count=0   # files changed outside docs/design/

while IFS= read -r -d '' status; do
	IFS= read -r -d '' path || true
	if [[ $path != docs/design/* ]]; then
		outside_count=$((outside_count + 1))
	elif [[ $path == docs/design/open-questions.md ]]; then
		continue
	elif [[ $status == A ]]; then
		added_design+=("$status $path")
	else
		offending+=("$status $path")
	fi
done <"$diff_file"

if ((${#offending[@]} == 0)); then
	if ((${#added_design[@]} == 0 || outside_count == 0)); then
		exit 0
	fi
	# Added design documents alongside files outside docs/design/.
	offending=("${added_design[@]}")
	ratification_mixed=1
fi

echo "ERROR: Design documents cannot be modified in implementation PRs."
echo "Only docs/design/open-questions.md may be changed."
if [[ -n "${ratification_mixed:-}" ]]; then
	echo "A ratification pull request must contain only design documents."
fi
echo ""
echo "Offending files (status path):"
for entry in "${offending[@]}"; do
	printf '  %s\n' "$entry"
done
exit 1
