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

# Fetch the base ref to ensure it's available
git fetch origin "${GITHUB_BASE_REF:-.}" --depth=1 2>/dev/null || true

# Get the list of changed files in docs/design/ excluding open-questions.md
changed_design_files=$(
	git diff --name-only "${base_ref}...HEAD" -- "docs/design/" 2>/dev/null | \
	grep -v "docs/design/open-questions.md" || true
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
