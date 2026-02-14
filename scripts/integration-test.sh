#!/bin/bash
# Integration test script for github-copier
# Sends test webhook payload to locally running app
#
# Usage:
#   ./scripts/integration-test.sh webhook   # Send test webhook
#   ./scripts/integration-test.sh verify    # Check dest repos
#   ./scripts/integration-test.sh full      # Both + wait
#
# Environment:
#   APP_URL         - App URL (default: http://localhost:8080)
#   WEBHOOK_SECRET  - Webhook secret (default: reads from .env.test)
#   PAYLOAD_FILE    - Payload file (default: testdata/test-pr-merged.json)

set -e

# Load webhook secret from .env.test if it exists and WEBHOOK_SECRET not set
if [[ -z "$WEBHOOK_SECRET" && -f ".env.test" ]]; then
    WEBHOOK_SECRET=$(grep -E "^WEBHOOK_SECRET=" .env.test | cut -d'=' -f2- | tr -d '"' | tr -d "'")
fi

# Configuration
APP_URL="${APP_URL:-http://localhost:8080}"
WEBHOOK_SECRET="${WEBHOOK_SECRET:-test-secret}"
PAYLOAD_FILE="${PAYLOAD_FILE:-testdata/test-pr-merged.json}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Check if app is running
check_app() {
    log_info "Checking if app is running at $APP_URL..."
    if curl -s "$APP_URL/health" > /dev/null 2>&1; then
        log_info "App is running"
        return 0
    else
        log_error "App is not running at $APP_URL"
        log_info "Start the app with: go run app.go"
        return 1
    fi
}

# Generate HMAC signature for webhook
generate_signature() {
    local payload="$1"
    echo -n "$payload" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | sed 's/^.* //'
}

# Send webhook payload
send_webhook() {
    local payload_file="$1"
    
    if [[ ! -f "$payload_file" ]]; then
        log_error "Payload file not found: $payload_file"
        return 1
    fi
    
    local payload=$(cat "$payload_file")
    local signature="sha256=$(generate_signature "$payload")"
    
    log_info "Sending webhook payload from $payload_file..."
    log_info "Signature: $signature"
    
    response=$(curl -s -w "\n%{http_code}" \
        -X POST "$APP_URL/events" \
        -H "Content-Type: application/json" \
        -H "X-GitHub-Event: pull_request" \
        -H "X-Hub-Signature-256: $signature" \
        -d "$payload")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [[ "$http_code" == "200" ]]; then
        log_info "Webhook accepted (HTTP $http_code)"
        echo "$body"
        return 0
    else
        log_error "Webhook rejected (HTTP $http_code)"
        echo "$body"
        return 1
    fi
}

# Verify files in destination repo (requires gh CLI)
verify_dest_repo() {
    local repo="$1"
    local path="$2"
    
    log_info "Checking $repo for $path..."
    if gh api "repos/$repo/contents/$path" > /dev/null 2>&1; then
        log_info "✓ Found $path in $repo"
        return 0
    else
        log_warn "✗ Not found: $path in $repo"
        return 1
    fi
}

# Main
main() {
    echo "=========================================="
    echo "GitHub Copier Integration Test"
    echo "=========================================="
    
    case "${1:-webhook}" in
        webhook)
            check_app || exit 1
            send_webhook "$PAYLOAD_FILE"
            ;;
        verify)
            log_info "Verifying destination repos..."
            # Update these to match your test workflow destinations
            verify_dest_repo "${DEST_REPO_1:-your-org/dest-repo-1}" "${DEST_PATH_1:-examples}"
            verify_dest_repo "${DEST_REPO_2:-your-org/dest-repo-2}" "${DEST_PATH_2:-examples}"
            ;;
        full)
            check_app || exit 1
            send_webhook "$PAYLOAD_FILE"
            log_info "Waiting 10s for processing..."
            sleep 10
            verify_dest_repo "${DEST_REPO_1:-your-org/dest-repo-1}" "${DEST_PATH_1:-examples}"
            verify_dest_repo "${DEST_REPO_2:-your-org/dest-repo-2}" "${DEST_PATH_2:-examples}"
            ;;
        *)
            echo "Usage: $0 [webhook|verify|full]"
            echo "  webhook - Send test webhook to app (default)"
            echo "  verify  - Check destination repos for expected files"
            echo "  full    - Send webhook and verify results"
            exit 1
            ;;
    esac
}

main "$@"

