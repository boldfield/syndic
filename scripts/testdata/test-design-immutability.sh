#!/bin/bash
set -euo pipefail

test_dir=$(mktemp -d)
trap "rm -rf $test_dir" EXIT

cd "$test_dir"
git init
git config user.email "test@example.com"
git config user.name "Test User"

# Create design files
mkdir -p docs/design
echo "# Design doc" > docs/design/v4-delta.md
echo "# Open questions" > docs/design/open-questions.md

git add .
git commit -m "Initial"

# Test 1: Modifying only open-questions.md should pass
echo "Test 1: Modify open-questions.md only"
echo "# New content" >> docs/design/open-questions.md
git add docs/design/open-questions.md
git commit -m "Update open questions"
GITHUB_BASE_REF=HEAD~1 bash ../check-design-immutability.sh && echo "✓ PASS" || echo "✗ FAIL"

# Reset for next test
git reset --hard HEAD~1

# Test 2: Modifying protected design doc should fail
echo "Test 2: Modify protected design doc"
echo "# Modified" >> docs/design/v4-delta.md
git add docs/design/v4-delta.md
git commit -m "Update design"
if GITHUB_BASE_REF=HEAD~1 bash ../check-design-immutability.sh; then
  echo "✗ FAIL - Expected failure but script succeeded"
  exit 1
else
  echo "✓ PASS - Script failed as expected"
fi
