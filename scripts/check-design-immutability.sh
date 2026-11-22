#!/usr/bin/env bash
set -euo pipefail

# Check that design documents (except open-questions.md) are not modified in pull requests.
# This enforces the interim reading of the Process clause in v5-delta-conversations-revision-tools.md.
#
# A pull request passes when either:
# 1. It changes no file under docs/design/ other than open-questions.md (unchanged rule).
# 2. It is a ratification: every file it changes is under docs/design/, every one of those
#    other than open-questions.md is newly added (not modified/deleted/renamed), it modifies,
#    deletes and renames no existing design document, and it changes nothing outside docs/design/.
#
# See OQ-6 in docs/design/open-questions.md.

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

# Get all changed files with status codes (A=added, M=modified, D=deleted)
# --no-renames treats renames as separate deletion and addition
all_changes=$(
	git diff --name-status --no-renames "${base_ref}...HEAD"
)

# Separate design and non-design changes
design_changes=$(echo "$all_changes" | grep "^[AMD]	docs/design/" || true)
non_design_changes=$(echo "$all_changes" | grep -v "^[AMD]	docs/design/" || true)

# Check if there are any non-design changes
if [[ -n "$non_design_changes" ]]; then
	# There are changes outside docs/design/, so not a ratification
	# Check if there are problematic design changes
	problematic_design=$(echo "$design_changes" | grep -v "^A	docs/design/open-questions.md" | grep -v "^M	docs/design/open-questions.md" || true)

	if [[ -n "$problematic_design" ]]; then
		echo "ERROR: Design documents cannot be modified in implementation PRs."
		echo "Only docs/design/open-questions.md may be changed."
		echo ""
		echo "Modified files:"
		echo "$problematic_design" | sed 's/^[A-Z]	/  /'
		exit 1
	fi
	# If only non-problematic design or no design changes, pass
	exit 0
fi

# All changes are under docs/design/
# Check if it's a valid ratification (all changes are either additions or modifications to open-questions.md)
if [[ -z "$design_changes" ]]; then
	# No design changes at all, pass
	exit 0
fi

# Check each design change
problematic_files=""
added_design_docs=""

while IFS=$'\t' read -r status file; do
	if [[ "$file" == "docs/design/open-questions.md" ]]; then
		# open-questions.md can be added or modified
		continue
	elif [[ "$status" == "A" ]]; then
		# Newly added design document is OK in ratification
		added_design_docs="${added_design_docs}${file}
"
	else
		# Modified (M), deleted (D), or renamed (R) design document fails
		problematic_files="${problematic_files}${status}	${file}
"
	fi
done <<< "$design_changes"

if [[ -n "$problematic_files" ]]; then
	echo "ERROR: Design documents cannot be modified in implementation PRs."
	echo "Only docs/design/open-questions.md may be changed."
	echo ""
	echo "Modified files:"
	echo "$problematic_files" | sed 's/^[A-Z]	/  /'
	exit 1
fi

# If we got here, it's either:
# - No design changes at all, or
# - Only additions of new design documents + possible open-questions.md changes
# This is valid (either case 1 or case 2 of the interim reading)
exit 0
