#!/bin/bash
# Create a versioned release: update CHANGELOG, commit, tag, push, and create GitHub Release.
#
# Usage:
#   ./scripts/release.sh <version>
#   ./scripts/release.sh v1.0.0
#   ./scripts/release.sh v0.5.1 --dry-run   # show what would happen without making changes
#
# Prerequisites:
#   - git (with a clean working tree on main branch)
#   - gh (GitHub CLI, authenticated)
#   - CHANGELOG.md with an [Unreleased] section (Keep a Changelog format)
#
# What it does:
#   1. Validates the version tag format (vMAJOR.MINOR.PATCH)
#   2. Checks that the working tree is clean and on the main branch
#   3. Renames [Unreleased] → [vX.Y.Z] - YYYY-MM-DD in CHANGELOG.md
#   4. Adds a fresh [Unreleased] section
#   5. Commits the CHANGELOG update
#   6. Creates an annotated git tag
#   7. Pushes the tag (triggers CI deploy)
#   8. Creates a GitHub Release with the changelog excerpt

set -euo pipefail

# ─── Colors ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ─── Helpers ─────────────────────────────────────────────────────────────────
info()  { echo -e "${BLUE}ℹ ${NC}$*"; }
ok()    { echo -e "${GREEN}✓ ${NC}$*"; }
warn()  { echo -e "${YELLOW}⚠ ${NC}$*"; }
err()   { echo -e "${RED}✗ ${NC}$*" >&2; }
die()   { err "$@"; exit 1; }

# ─── Parse arguments ────────────────────────────────────────────────────────
DRY_RUN=false
VERSION=""

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    v*)        VERSION="$arg" ;;
    *)         die "Unknown argument: $arg" ;;
  esac
done

if [ -z "$VERSION" ]; then
  echo "Usage: $0 <version> [--dry-run]"
  echo ""
  echo "Examples:"
  echo "  $0 v1.0.0"
  echo "  $0 v0.5.1 --dry-run"
  exit 1
fi

# ─── Validate version format ────────────────────────────────────────────────
if ! echo "$VERSION" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  die "Invalid version format: $VERSION (expected vMAJOR.MINOR.PATCH, e.g. v1.0.0)"
fi

# ─── Validate prerequisites ─────────────────────────────────────────────────
command -v git >/dev/null 2>&1 || die "git is required"
command -v gh  >/dev/null 2>&1 || die "gh (GitHub CLI) is required"

# Move to repo root
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || die "Not inside a git repository"
cd "$REPO_ROOT"

# Check clean working tree
if ! git diff --quiet || ! git diff --cached --quiet; then
  die "Working tree is not clean. Commit or stash changes first."
fi

# Check we're on main
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [ "$BRANCH" != "main" ]; then
  die "Must be on main branch (currently on: $BRANCH)"
fi

# Check tag doesn't already exist
if git rev-parse "$VERSION" >/dev/null 2>&1; then
  die "Tag $VERSION already exists"
fi

# Check CHANGELOG.md exists and has [Unreleased]
CHANGELOG="CHANGELOG.md"
if [ ! -f "$CHANGELOG" ]; then
  die "$CHANGELOG not found"
fi

if ! grep -q '^\## \[Unreleased\]' "$CHANGELOG"; then
  die "$CHANGELOG does not contain an [Unreleased] section"
fi

# Check that [Unreleased] has content (not just the heading)
UNRELEASED_CONTENT=$(sed -n '/^## \[Unreleased\]/,/^## \[/{/^## \[/d;p;}' "$CHANGELOG" | grep -v '^$' | head -1)
if [ -z "$UNRELEASED_CONTENT" ]; then
  die "[Unreleased] section in $CHANGELOG is empty. Nothing to release."
fi

# ─── Show plan ───────────────────────────────────────────────────────────────
TODAY=$(date +%Y-%m-%d)

echo ""
echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  Release: ${VERSION}$(printf '%*s' $((42 - ${#VERSION})) '')  ║${NC}"
echo -e "${BLUE}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BLUE}║${NC}  Date:     ${TODAY}                                    ${BLUE}║${NC}"
echo -e "${BLUE}║${NC}  Branch:   ${BRANCH}                                        ${BLUE}║${NC}"
echo -e "${BLUE}║${NC}  Dry run:  ${DRY_RUN}                                      ${BLUE}║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

if [ "$DRY_RUN" = true ]; then
  warn "Dry run mode — no changes will be made"
  echo ""
fi

# ─── Step 1: Update CHANGELOG ───────────────────────────────────────────────
info "Updating $CHANGELOG..."

# Replace [Unreleased] with versioned heading and add a new [Unreleased] above it
REPLACEMENT="## [Unreleased]\n\n## [${VERSION}] - ${TODAY}"

if [ "$DRY_RUN" = true ]; then
  info "Would replace '## [Unreleased]' with:"
  echo -e "  $REPLACEMENT"
else
  # Use perl for reliable in-place replacement (works on macOS and Linux)
  perl -i -pe "s/^## \\[Unreleased\\]/## [Unreleased]\n\n## [${VERSION}] - ${TODAY}/" "$CHANGELOG"
  ok "Updated $CHANGELOG"
fi

# ─── Step 2: Extract release notes ──────────────────────────────────────────
info "Extracting release notes for $VERSION..."

# Extract the section between this version and the next ## heading
RELEASE_NOTES=$(sed -n "/^## \[${VERSION}\]/,/^## \[/{/^## \[${VERSION}\]/d;/^## \[/d;p;}" "$CHANGELOG" 2>/dev/null || true)

# If dry run, extract from the [Unreleased] section instead (CHANGELOG not yet modified)
if [ "$DRY_RUN" = true ]; then
  RELEASE_NOTES=$(sed -n '/^## \[Unreleased\]/,/^## \[/{/^## \[/d;p;}' "$CHANGELOG")
fi

# Trim leading/trailing whitespace (using perl for macOS compatibility)
RELEASE_NOTES=$(echo "$RELEASE_NOTES" | perl -0777 -pe 's/^\s+//; s/\s+$//')

if [ -z "$RELEASE_NOTES" ]; then
  warn "No release notes extracted — the GitHub Release will have minimal content"
fi

# ─── Step 3: Commit, tag, push ──────────────────────────────────────────────
if [ "$DRY_RUN" = true ]; then
  info "Would commit: 'Release $VERSION'"
  info "Would create annotated tag: $VERSION"
  info "Would push tag to origin"
  info "Would create GitHub Release: $VERSION"
  echo ""
  info "Release notes preview:"
  echo "---"
  echo "$RELEASE_NOTES"
  echo "---"
  echo ""
  ok "Dry run complete. Run without --dry-run to execute."
  exit 0
fi

info "Committing CHANGELOG update..."
git add "$CHANGELOG"
git commit -m "$(cat <<EOF
Release ${VERSION}

EOF
)"
ok "Committed"

info "Creating annotated tag $VERSION..."
git tag -a "$VERSION" -m "Release $VERSION"
ok "Tag created"

info "Pushing tag to origin..."
git push origin main
git push origin "$VERSION"
ok "Pushed (CI deploy will be triggered by the tag)"

# ─── Step 4: Create GitHub Release ──────────────────────────────────────────
info "Creating GitHub Release..."
gh release create "$VERSION" \
  --title "$VERSION" \
  --notes "$RELEASE_NOTES"
ok "GitHub Release created"

echo ""
echo -e "${GREEN}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  Release $VERSION complete!$(printf '%*s' $((37 - ${#VERSION})) '')  ║${NC}"
echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║${NC}  • CHANGELOG.md updated                                  ${GREEN}║${NC}"
echo -e "${GREEN}║${NC}  • Tag $VERSION pushed                                   ${GREEN}║${NC}"
echo -e "${GREEN}║${NC}  • GitHub Release created                                ${GREEN}║${NC}"
echo -e "${GREEN}║${NC}  • CI deploy triggered — monitor at:                     ${GREEN}║${NC}"
echo -e "${GREEN}║${NC}    $(gh repo view --json url -q .url)/actions             ${GREEN}║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════╝${NC}"
