#!/bin/bash
# Bump version across all synchronized packages

set -e

BUMP_TYPE="${1:-patch}"

if [[ ! "$BUMP_TYPE" =~ ^(patch|minor|major)$ ]]; then
    echo "❌ Invalid bump type: $BUMP_TYPE"
    echo "Usage: bump-version.sh [patch|minor|major]"
    exit 1
fi

# Get current version from root package.json
CURRENT_VERSION=$(node -p "require('./package.json').version")
echo "📍 Current version: $CURRENT_VERSION"

# Parse version parts
IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

# Calculate new version
case $BUMP_TYPE in
    patch)
        NEW_PATCH=$((PATCH + 1))
        NEW_VERSION="$MAJOR.$MINOR.$NEW_PATCH"
        ;;
    minor)
        NEW_MINOR=$((MINOR + 1))
        NEW_VERSION="$MAJOR.$NEW_MINOR.0"
        ;;
    major)
        NEW_MAJOR=$((MAJOR + 1))
        NEW_VERSION="$NEW_MAJOR.0.0"
        ;;
esac

echo "🚀 New version: $NEW_VERSION ($BUMP_TYPE)"
echo ""

# Packages to update (synchronized versions)
PACKAGES=(
    "package.json"
    "packages/client/package.json"
    "packages/cloud/package.json"
    "packages/desktop/package.json"
    "packages/server/package.json"
    "packages/shared/package.json"
    "packages/terminal-service-rs/package.json"
    "packages/conductor-cli-wrapper/package.json"
)

echo "📦 Updating packages:"

for PKG in "${PACKAGES[@]}"; do
    if [ -f "$PKG" ]; then
        # Use node to update version (handles JSON properly)
        node -e "
            const fs = require('fs');
            const pkg = JSON.parse(fs.readFileSync('$PKG', 'utf8'));
            pkg.version = '$NEW_VERSION';
            fs.writeFileSync('$PKG', JSON.stringify(pkg, null, 2) + '\n');
        "
        echo "   ✅ $PKG → $NEW_VERSION"
    else
        echo "   ⚠️  $PKG (not found)"
    fi
done

echo ""
echo "✅ Version bumped to $NEW_VERSION"
echo ""
echo "📝 Next steps:"
echo "   1. Review changes: git diff"
echo "   2. Commit: git add -A && git commit -m 'chore: bump version to $NEW_VERSION'"
echo "   3. Tag: git tag -a 'v$NEW_VERSION' -m 'Release v$NEW_VERSION'"
echo "   4. Push: git push origin main --follow-tags"
