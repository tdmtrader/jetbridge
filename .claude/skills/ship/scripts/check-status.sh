#!/bin/bash
# Check git status for shipping

set -e

echo "=== Git Status Check ==="
echo ""

# Current branch
BRANCH=$(git branch --show-current)
echo "📍 Branch: $BRANCH"
echo ""

# Check for uncommitted changes
MODIFIED=$(git diff --name-only 2>/dev/null | wc -l | tr -d ' ')
STAGED=$(git diff --cached --name-only 2>/dev/null | wc -l | tr -d ' ')
UNTRACKED=$(git ls-files --others --exclude-standard 2>/dev/null | wc -l | tr -d ' ')

echo "📊 Changes Summary:"
echo "   Modified (unstaged): $MODIFIED"
echo "   Staged: $STAGED"
echo "   Untracked: $UNTRACKED"
echo ""

TOTAL=$((MODIFIED + STAGED + UNTRACKED))

if [ "$TOTAL" -eq 0 ]; then
    echo "✅ Working directory is clean - nothing to ship"
    exit 0
fi

# Show details
if [ "$UNTRACKED" -gt 0 ]; then
    echo "📁 Untracked files:"
    git ls-files --others --exclude-standard | head -15
    if [ "$UNTRACKED" -gt 15 ]; then
        echo "   ... and $((UNTRACKED - 15)) more"
    fi
    echo ""
fi

if [ "$MODIFIED" -gt 0 ]; then
    echo "📝 Modified files:"
    git diff --name-only | head -15
    if [ "$MODIFIED" -gt 15 ]; then
        echo "   ... and $((MODIFIED - 15)) more"
    fi
    echo ""
fi

if [ "$STAGED" -gt 0 ]; then
    echo "✅ Staged files:"
    git diff --cached --name-only | head -15
    if [ "$STAGED" -gt 15 ]; then
        echo "   ... and $((STAGED - 15)) more"
    fi
    echo ""
fi

echo "📦 Total files to ship: $TOTAL"
