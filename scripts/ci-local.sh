#!/bin/bash

# Run the full CI pipeline locally before pushing
# Mirrors what .github/workflows/ci.yml does
#
# Usage: ./scripts/ci-local.sh

set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

PASS="${GREEN}PASS${NC}"
FAIL="${RED}FAIL${NC}"

echo -e "${BLUE}Running local CI pipeline...${NC}"
echo ""

# 1. Build
echo -n "Build...        "
if go build ./... 2>&1; then
    echo -e "$PASS"
else
    echo -e "$FAIL"
    exit 1
fi

# 2. Test with race detector
echo -n "Test (race)...  "
if go test -race ./... 2>&1; then
    echo -e "$PASS"
else
    echo -e "$FAIL"
    exit 1
fi

# 3. Lint
echo -n "Lint...         "
if command -v golangci-lint &> /dev/null; then
    if golangci-lint run ./... 2>&1; then
        echo -e "$PASS"
    else
        echo -e "$FAIL"
        exit 1
    fi
else
    echo -e "${RED}SKIP${NC} (golangci-lint not installed)"
    echo "  Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
fi

# 4. Vet
echo -n "Vet...          "
if go vet ./... 2>&1; then
    echo -e "$PASS"
else
    echo -e "$FAIL"
    exit 1
fi

echo ""
echo -e "${GREEN}All checks passed!${NC}"
