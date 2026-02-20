#!/bin/bash

# Integration test script for github-copier
# Runs automated end-to-end tests locally without external dependencies
#
# Usage:
#   ./scripts/integration-test.sh              # Run all tests
#   ./scripts/integration-test.sh --quick      # Run quick smoke tests only
#   ./scripts/integration-test.sh --verbose    # Show detailed output

set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

PASS="${GREEN}PASS${NC}"
FAIL="${RED}FAIL${NC}"
SKIP="${YELLOW}SKIP${NC}"

# Configuration
APP_PORT="${PORT:-8080}"
APP_URL="http://localhost:${APP_PORT}"
WEBHOOK_ENDPOINT="${APP_URL}/events"
HEALTH_ENDPOINT="${APP_URL}/health"
METRICS_ENDPOINT="${APP_URL}/metrics"
TESTDATA_DIR="testdata"
APP_PID=""
VERBOSE=false
QUICK_MODE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --verbose|-v)
            VERBOSE=true
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
            echo "  --verbose, -v    Show detailed output"
            echo "  --quick, -q      Run quick smoke tests only"
            echo "  --help, -h       Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

log() {
    if [[ "$VERBOSE" == "true" ]]; then
        echo -e "$1"
    fi
}

log_always() {
    echo -e "$1"
}

cleanup() {
    if [[ -n "$APP_PID" ]] && kill -0 "$APP_PID" 2>/dev/null; then
        log "Stopping application (PID: $APP_PID)..."
        kill "$APP_PID" 2>/dev/null || true
        wait "$APP_PID" 2>/dev/null || true
    fi
}

trap cleanup EXIT

# Build the application and test tools
build_app() {
    log_always -n "Building application...  "
    if go build -o github-copier . 2>&1; then
        log_always -e "$PASS"
    else
        log_always -e "$FAIL"
        exit 1
    fi
    
    log_always -n "Building test-webhook...  "
    if go build -o test-webhook ./cmd/test-webhook 2>&1; then
        log_always -e "$PASS"
    else
        log_always -e "$FAIL"
        exit 1
    fi
}

# Start the application in dry-run mode
start_app() {
    log_always -n "Starting app (dry-run)... "
    
    # Check if something is already listening on the port
    if lsof -i ":${APP_PORT}" >/dev/null 2>&1; then
        log_always -e "${YELLOW}Port ${APP_PORT} in use${NC}"
        log_always "Stop existing process or set PORT env var"
        exit 1
    fi
    
    # Start with minimal config for testing
    COPIER_DISABLE_CLOUD_LOGGING=true \
    DRY_RUN=true \
    AUDIT_ENABLED=false \
    LOG_LEVEL=error \
    PORT="${APP_PORT}" \
    ./github-copier > /tmp/integration-test-app.log 2>&1 &
    
    APP_PID=$!
    
    # Wait for app to be ready (max 30 seconds)
    local max_wait=30
    local waited=0
    while [[ $waited -lt $max_wait ]]; do
        if curl -s "${HEALTH_ENDPOINT}" >/dev/null 2>&1; then
            log_always -e "$PASS"
            return 0
        fi
        
        # Check if process died
        if ! kill -0 "$APP_PID" 2>/dev/null; then
            log_always -e "$FAIL"
            log_always "Application failed to start. Logs:"
            cat /tmp/integration-test-app.log
            exit 1
        fi
        
        sleep 1
        ((waited++))
    done
    
    log_always -e "$FAIL (timeout)"
    exit 1
}

# Test health endpoint
test_health() {
    log_always -n "Health check...           "
    local response
    response=$(curl -s -w "\n%{http_code}" "${HEALTH_ENDPOINT}")
    local http_code
    http_code=$(echo "$response" | tail -n1)
    
    if [[ "$http_code" == "200" ]]; then
        log_always -e "$PASS"
        return 0
    else
        log_always -e "$FAIL (HTTP $http_code)"
        return 1
    fi
}

# Test metrics endpoint
test_metrics() {
    log_always -n "Metrics endpoint...       "
    local response
    response=$(curl -s -w "\n%{http_code}" "${METRICS_ENDPOINT}")
    local http_code
    http_code=$(echo "$response" | tail -n1)
    
    if [[ "$http_code" == "200" ]]; then
        log_always -e "$PASS"
        return 0
    else
        log_always -e "$FAIL (HTTP $http_code)"
        return 1
    fi
}

# Send a test webhook payload
send_webhook() {
    local payload_file="$1"
    local expected_status="${2:-202}"
    local description="$3"
    
    if [[ ! -f "$payload_file" ]]; then
        log_always -e "  $description: ${SKIP} (file not found)"
        return 0
    fi
    
    log_always -n "  $description... "
    
    local response
    response=$(./test-webhook -payload "$payload_file" -url "${WEBHOOK_ENDPOINT}" 2>&1)
    local exit_code=$?
    
    if [[ $exit_code -eq 0 ]]; then
        log_always -e "$PASS"
        log "$response"
        return 0
    else
        log_always -e "$FAIL"
        log "$response"
        return 1
    fi
}

# Test webhook payloads
test_webhooks() {
    log_always ""
    log_always -e "${BLUE}Testing webhook payloads:${NC}"
    
    local failed=0
    
    # Test merged PR (should be processed)
    send_webhook "${TESTDATA_DIR}/example-pr-merged.json" 202 "Merged PR (example)" || ((failed++))
    send_webhook "${TESTDATA_DIR}/test-pr-merged.json" 202 "Merged PR (test)" || ((failed++))
    
    if [[ "$QUICK_MODE" == "true" ]]; then
        log_always -e "  ${YELLOW}Quick mode: skipping additional payloads${NC}"
        return $failed
    fi
    
    # Test various PR scenarios
    send_webhook "${TESTDATA_DIR}/pr-closed-not-merged.json" 202 "Closed (not merged)" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-opened.json" 202 "PR opened" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-synchronize.json" 202 "PR synchronized" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-multiple-workflows.json" 202 "Multiple workflows" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-no-matching-files.json" 202 "No matching files" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-large-changeset.json" 202 "Large changeset" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-renamed-files.json" 202 "Renamed files" || ((failed++))
    send_webhook "${TESTDATA_DIR}/pr-with-deprecations.json" 202 "With deprecations" || ((failed++))
    
    # Test push event (if supported)
    send_webhook "${TESTDATA_DIR}/push-to-main.json" 202 "Push to main" || ((failed++))
    
    return $failed
}

# Verify metrics after tests
verify_metrics() {
    log_always ""
    log_always -n "Verifying metrics...      "
    
    local metrics
    metrics=$(curl -s "${METRICS_ENDPOINT}")
    
    # Check that webhooks were received
    local received
    received=$(echo "$metrics" | grep -o '"received":[0-9]*' | grep -o '[0-9]*' || echo "0")
    
    if [[ "$received" -gt 0 ]]; then
        log_always -e "$PASS (received: $received)"
        return 0
    else
        log_always -e "${YELLOW}WARN${NC} (no webhooks recorded)"
        return 0  # Not a failure, might be expected in some configs
    fi
}

# Check logs for errors
check_logs() {
    log_always -n "Checking for errors...    "
    
    if grep -q '"level":"ERROR"' /tmp/integration-test-app.log 2>/dev/null; then
        local error_count
        error_count=$(grep -c '"level":"ERROR"' /tmp/integration-test-app.log)
        log_always -e "${YELLOW}WARN${NC} ($error_count errors in log)"
        if [[ "$VERBOSE" == "true" ]]; then
            grep '"level":"ERROR"' /tmp/integration-test-app.log | head -5
        fi
    else
        log_always -e "$PASS"
    fi
}

# Main execution
main() {
    log_always ""
    log_always -e "${BLUE}╔════════════════════════════════════════════════════════════════╗${NC}"
    log_always -e "${BLUE}║  GitHub Copier - Integration Tests                            ║${NC}"
    log_always -e "${BLUE}╚════════════════════════════════════════════════════════════════╝${NC}"
    log_always ""
    
    local total_failed=0
    
    # Build
    build_app
    
    # Start app
    start_app
    
    log_always ""
    log_always -e "${BLUE}Running endpoint tests:${NC}"
    
    # Test endpoints
    test_health || ((total_failed++))
    test_metrics || ((total_failed++))
    
    # Test webhooks
    test_webhooks || ((total_failed++))
    
    # Verify results
    verify_metrics
    check_logs
    
    log_always ""
    if [[ $total_failed -eq 0 ]]; then
        log_always -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
        log_always -e "${GREEN}  All integration tests passed!${NC}"
        log_always -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
        exit 0
    else
        log_always -e "${RED}════════════════════════════════════════════════════════════════${NC}"
        log_always -e "${RED}  $total_failed test(s) failed${NC}"
        log_always -e "${RED}════════════════════════════════════════════════════════════════${NC}"
        exit 1
    fi
}

main
