#!/usr/bin/env bash
set -euo pipefail

# Check that design documents (except open-questions.md) are not modified
# in pull requests. This enforces the adopted Process clause in the v5 delta.

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

# Get the list of changed files in docs/design/ excluding open-questions.md
# We separate git diff from grep so we only tolerate grep's exit status (1 for no match),
# not git's errors. This prevents failing open when git diff itself errors.
changed_design_files=$(
	git diff --name-only --no-renames "${base_ref}...HEAD" -- "docs/design/"
)
changed_design_files=$(
	echo "$changed_design_files" | grep -vxF "docs/design/open-questions.md" || true
)

if [[ -n "$changed_design_files" ]]; then
	echo "ERROR: Design documents cannot be modified in implementation PRs."
	echo "Only docs/design/open-questions.md may be changed."
	echo ""
	echo "Modified files:"
	echo "$changed_design_files" | sed 's/^/  /'
	exit 1
fi

exit 0
