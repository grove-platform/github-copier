#!/bin/bash

# Run the full CI pipeline locally before pushing
# Mirrors what .github/workflows/ci.yml does
#
# Usage:
#   ./scripts/ci-local.sh           # Run standard CI checks
#   ./scripts/ci-local.sh --full    # Include integration tests
#   ./scripts/ci-local.sh --quick   # Fast checks only (no race detector)

set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

PASS="${GREEN}PASS${NC}"
FAIL="${RED}FAIL${NC}"
SKIP="${YELLOW}SKIP${NC}"

# Options
RUN_INTEGRATION=false
QUICK_MODE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --full|-f)
            RUN_INTEGRATION=true
            shift
            ;;
        --quick|-q)
            QUICK_MODE=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --full, -f     Include integration tests"
            echo "  --quick, -q    Fast checks only (no race detector)"
            echo "  --help, -h     Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo -e "${BLUE}╔════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  GitHub Copier - Local CI Pipeline                            ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════════╝${NC}"
echo ""

FAILED=0

# 1. Build
echo -n "Build...              "
if go build ./... 2>&1; then
    echo -e "$PASS"
else
    echo -e "$FAIL"
    ((FAILED++))
fi

# 2. Test with race detector (skip in quick mode)
if [[ "$QUICK_MODE" == "true" ]]; then
    echo -n "Test (no race)...     "
    if go test ./... 2>&1; then
        echo -e "$PASS"
    else
        echo -e "$FAIL"
        ((FAILED++))
    fi
else
    echo -n "Test (race)...        "
    if go test -race ./... 2>&1; then
        echo -e "$PASS"
    else
        echo -e "$FAIL"
        ((FAILED++))
    fi
fi

# 3. Lint
echo -n "Lint...               "
if command -v golangci-lint &> /dev/null; then
    if golangci-lint run ./... 2>&1; then
        echo -e "$PASS"
    else
        echo -e "$FAIL"
        ((FAILED++))
    fi
else
    echo -e "$SKIP (golangci-lint not installed)"
    echo "  Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
fi

# 4. Vet
echo -n "Vet...                "
if go vet ./... 2>&1; then
    echo -e "$PASS"
else
    echo -e "$FAIL"
    ((FAILED++))
fi

# 5. Test coverage (optional, quick summary)
if [[ "$QUICK_MODE" != "true" ]]; then
    echo -n "Coverage...           "
    COVERAGE=$(go test ./services -cover 2>&1 | grep -o 'coverage: [0-9.]*%' | grep -o '[0-9.]*' || echo "0")
    if [[ -n "$COVERAGE" ]]; then
        echo -e "${GREEN}${COVERAGE}%${NC}"
    else
        echo -e "$SKIP"
    fi
fi

# 6. Integration tests (if --full flag)
if [[ "$RUN_INTEGRATION" == "true" ]]; then
    echo ""
    echo -e "${BLUE}Running integration tests...${NC}"
    if [[ -x "./scripts/integration-test.sh" ]]; then
        if ./scripts/integration-test.sh --quick 2>&1; then
            echo -e "Integration tests... $PASS"
        else
            echo -e "Integration tests... $FAIL"
            ((FAILED++))
        fi
    else
        echo -e "Integration tests... $SKIP (script not found)"
    fi
fi

# Summary
echo ""
echo "════════════════════════════════════════════════════════════════"
if [[ $FAILED -eq 0 ]]; then
    echo -e "${GREEN}All checks passed!${NC}"
    exit 0
else
    echo -e "${RED}$FAILED check(s) failed${NC}"
    exit 1
fi
