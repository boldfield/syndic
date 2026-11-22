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

# Get all changed files with status codes using NUL-delimited format.
# -z produces NUL-terminated records: status NUL path NUL status NUL path NUL ...
# This avoids quoting issues with special characters (tabs, quotes, backslashes, etc.)
# --no-renames treats renames as separate deletion and addition.
all_changes=$(git diff -z --name-status --no-renames "${base_ref}...HEAD")

# Parse NUL-delimited git diff output into arrays
declare -a design_files
declare -a non_design_files

# Read NUL-terminated status and path pairs
while IFS= read -r -d '' status; do
	# Read the corresponding path
	if ! IFS= read -r -d '' path; then
		# Incomplete record at end of input
		break
	fi

	# Classify by actual path, not by regex
	if [[ "$path" == docs/design/* ]]; then
		design_files+=("$status" "$path")
	else
		non_design_files+=("$status" "$path")
	fi
done <<< "$all_changes"

# Check if there are any non-design changes
if [[ ${#non_design_files[@]} -gt 0 ]]; then
	# There are changes outside docs/design/, so not a ratification
	# Check for problematic design changes
	declare -a problematic_design
	declare -a added_design

	for ((i = 0; i < ${#design_files[@]}; i += 2)); do
		status="${design_files[i]}"
		path="${design_files[i+1]}"

		if [[ "$path" == "docs/design/open-questions.md" ]]; then
			# open-questions.md can be changed in any way when there are non-design changes
			continue
		elif [[ "$status" == "A" ]]; then
			# Newly added design document
			added_design+=("$status" "$path")
		else
			# Any other status (M, D, T, etc.) is problematic
			problematic_design+=("$status" "$path")
		fi
	done

	if [[ ${#problematic_design[@]} -gt 0 ]] || [[ ${#added_design[@]} -gt 0 ]]; then
		echo "ERROR: Design documents cannot be modified in implementation PRs."
		echo "Only docs/design/open-questions.md may be changed."
		echo ""
		if [[ ${#problematic_design[@]} -gt 0 ]]; then
			echo "Modified files:"
			for ((i = 0; i < ${#problematic_design[@]}; i += 2)); do
				status="${problematic_design[i]}"
				path="${problematic_design[i+1]}"
				echo "  ${status}	${path}"
			done
		fi
		if [[ ${#added_design[@]} -gt 0 ]]; then
			echo "A ratification pull request must contain only design documents."
		fi
		exit 1
	fi
	# If only non-problematic design or no design changes, pass
	exit 0
fi

# All changes are under docs/design/
# Check if it's a valid ratification (all changes are either additions of design docs or allowed changes to open-questions.md)
if [[ ${#design_files[@]} -eq 0 ]]; then
	# No design changes at all, pass
	exit 0
fi

# Check each design change
declare -a problematic_files

for ((i = 0; i < ${#design_files[@]}; i += 2)); do
	status="${design_files[i]}"
	path="${design_files[i+1]}"

	if [[ "$path" == "docs/design/open-questions.md" ]]; then
		# open-questions.md can be changed in any way in ratification
		continue
	elif [[ "$status" == "A" ]]; then
		# Newly added design document is OK in ratification
		continue
	else
		# Any status other than A is problematic (M, D, T, etc.)
		problematic_files+=("$status" "$path")
	fi
done

if [[ ${#problematic_files[@]} -gt 0 ]]; then
	echo "ERROR: Design documents cannot be modified in implementation PRs."
	echo "Only docs/design/open-questions.md may be changed."
	echo ""
	echo "Modified files:"
	for ((i = 0; i < ${#problematic_files[@]}; i += 2)); do
		status="${problematic_files[i]}"
		path="${problematic_files[i+1]}"
		echo "  ${status}	${path}"
	done
	exit 1
fi

# If we got here, it's either:
# - No design changes at all, or
# - Only additions of new design documents + possible open-questions.md changes
# This is valid (either case 1 or case 2 of the interim reading)
exit 0
