#!/bin/bash

# Test if the GitHub App can access the configured repository
# Checks the deployed Cloud Run service health and recent logs for errors

set -e

echo "Testing GitHub Repository Access"
echo "===================================="
echo ""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get configuration from env-cloudrun.yaml
ENV_FILE="env-cloudrun.yaml"
if [ ! -f "$ENV_FILE" ]; then
    echo -e "${RED}$ENV_FILE not found${NC}"
    echo "Create it: cp configs/env.yaml.production env-cloudrun.yaml"
    exit 1
fi

REPO_OWNER=$(grep "REPO_OWNER:" "$ENV_FILE" | grep -v "#" | awk '{print $2}' | tr -d '"')
REPO_NAME=$(grep "REPO_NAME:" "$ENV_FILE" | grep -v "#" | awk '{print $2}' | tr -d '"')
INSTALLATION_ID=$(grep "INSTALLATION_ID:" "$ENV_FILE" | grep -v "#" | awk '{print $2}' | tr -d '"')

echo "Configuration:"
echo "  Repository: $REPO_OWNER/$REPO_NAME"
echo "  Installation ID: $INSTALLATION_ID"
echo ""

# Get Cloud Run service URL
SERVICE_URL=$(gcloud run services describe github-copier --format="value(status.url)" 2>/dev/null)
if [ -z "$SERVICE_URL" ]; then
    echo -e "${RED}Cloud Run service 'github-copier' not found${NC}"
    exit 1
fi

echo "Service URL: $SERVICE_URL"
echo ""

# Check health endpoint
echo "Checking application health..."
HEALTH=$(curl -s "$SERVICE_URL/health")
echo "$HEALTH" | python3 -m json.tool 2>/dev/null || echo "$HEALTH"
echo ""

# Check readiness (includes GitHub auth check)
echo "Checking readiness (includes GitHub auth)..."
READY_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$SERVICE_URL/ready")
READY_BODY=$(curl -s "$SERVICE_URL/ready")

if [ "$READY_CODE" == "200" ]; then
    echo -e "${GREEN}Readiness check passed (HTTP $READY_CODE)${NC}"
    echo "$READY_BODY" | python3 -m json.tool 2>/dev/null || echo "$READY_BODY"
else
    echo -e "${RED}Readiness check failed (HTTP $READY_CODE)${NC}"
    echo "$READY_BODY" | python3 -m json.tool 2>/dev/null || echo "$READY_BODY"
fi
echo ""

# Check recent logs for errors
echo "Checking recent logs for errors..."
RECENT_ERRORS=$(gcloud run services logs read github-copier --limit=20 2>/dev/null | grep -i "401\|bad credentials\|unauthorized" || true)

if [ -z "$RECENT_ERRORS" ]; then
    echo -e "${GREEN}No recent 401 errors found${NC}"
else
    echo -e "${RED}Found recent authentication errors:${NC}"
    echo "$RECENT_ERRORS"
    echo ""
    echo "Possible causes:"
    echo "1. GitHub App is not installed on the repository"
    echo "2. Installation ID doesn't match the repository"
    echo "3. GitHub App private key is incorrect or expired"
    echo "4. GitHub App doesn't have required permissions (see github-app-manifest.yml)"
    echo ""
    echo "To fix:"
    echo "1. Go to: https://github.com/settings/installations"
    echo "2. Find your GitHub App installation"
    echo "3. Make sure $REPO_OWNER/$REPO_NAME is in the list of accessible repositories"
fi

echo ""
echo "Summary"
echo "=========="
echo "Repository: $REPO_OWNER/$REPO_NAME"
echo "Installation ID: $INSTALLATION_ID"
echo "Readiness: HTTP $READY_CODE"

if [ "$READY_CODE" == "200" ] && [ -z "$RECENT_ERRORS" ]; then
    echo -e "Status: ${GREEN}WORKING${NC}"
    exit 0
else
    echo -e "Status: ${RED}NEEDS ATTENTION${NC}"
    exit 1
fi
