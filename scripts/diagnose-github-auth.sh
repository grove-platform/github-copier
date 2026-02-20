#!/bin/bash

# Diagnostic script for GitHub App authentication issues
# This script helps diagnose 401 Bad credentials errors

set -e

echo "GitHub App Authentication Diagnostics"
echo "=========================================="
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if gcloud is installed
if ! command -v gcloud &> /dev/null; then
    echo -e "${RED}gcloud CLI not found${NC}"
    echo "Please install: https://cloud.google.com/sdk/docs/install"
    exit 1
fi

echo -e "${GREEN}gcloud CLI found${NC}"

# Get project info
PROJECT_ID=$(gcloud config get-value project 2>/dev/null)
if [ -z "$PROJECT_ID" ]; then
    echo -e "${RED}No GCP project set${NC}"
    echo "Run: gcloud config set project YOUR_PROJECT_ID"
    exit 1
fi

echo -e "${GREEN}GCP Project: $PROJECT_ID${NC}"

PROJECT_NUMBER=$(gcloud projects describe "$PROJECT_ID" --format="value(projectNumber)")
echo -e "${GREEN}Project Number: $PROJECT_NUMBER${NC}"

# Cloud Run uses the default Compute Engine SA unless a custom SA is configured
SERVICE_ACCOUNT="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
echo -e "   Service Account: $SERVICE_ACCOUNT"
echo ""

# Check Secret Manager API
echo "Checking Secret Manager..."
if gcloud services list --enabled --filter="name:secretmanager.googleapis.com" --format="value(name)" | grep -q secretmanager; then
    echo -e "${GREEN}Secret Manager API enabled${NC}"
else
    echo -e "${RED}Secret Manager API not enabled${NC}"
    echo "Run: gcloud services enable secretmanager.googleapis.com"
    exit 1
fi

# Check if secrets exist
echo ""
echo "Checking Secrets..."

check_secret() {
    local secret_name=$1
    if gcloud secrets describe "$secret_name" &>/dev/null; then
        echo -e "${GREEN}Secret exists: $secret_name${NC}"
        
        # Check IAM permissions
        if gcloud secrets get-iam-policy "$secret_name" --format="value(bindings.members)" | grep -q "$SERVICE_ACCOUNT"; then
            echo -e "${GREEN}   Service account has access${NC}"
        else
            echo -e "${RED}   Service account does NOT have access${NC}"
            echo -e "${YELLOW}   Fix: gcloud secrets add-iam-policy-binding $secret_name --member=\"serviceAccount:${SERVICE_ACCOUNT}\" --role=\"roles/secretmanager.secretAccessor\"${NC}"
        fi
    else
        echo -e "${RED}Secret NOT found: $secret_name${NC}"
    fi
}

check_secret "CODE_COPIER_PEM"
check_secret "webhook-secret"

# Check if we can access the PEM key
echo ""
echo "Checking GitHub App Private Key..."
if gcloud secrets versions access latest --secret=CODE_COPIER_PEM &>/dev/null; then
    PEM_FIRST_LINE=$(gcloud secrets versions access latest --secret=CODE_COPIER_PEM | head -n 1)
    if [[ "$PEM_FIRST_LINE" == "-----BEGIN RSA PRIVATE KEY-----" ]] || [[ "$PEM_FIRST_LINE" == "-----BEGIN PRIVATE KEY-----" ]]; then
        echo -e "${GREEN}Private key format looks correct${NC}"
    else
        echo -e "${RED}Private key format looks incorrect${NC}"
        echo "   First line: $PEM_FIRST_LINE"
    fi
else
    echo -e "${RED}Cannot access private key${NC}"
fi

# Check env-cloudrun.yaml
echo ""
echo "Checking env-cloudrun.yaml configuration..."
ENV_FILE="env-cloudrun.yaml"
if [ -f "$ENV_FILE" ]; then
    echo -e "${GREEN}$ENV_FILE found${NC}"
    
    # Extract values (plain YAML, no env_variables wrapper)
    GITHUB_APP_ID=$(grep "GITHUB_APP_ID:" "$ENV_FILE" | awk '{print $2}' | tr -d '"')
    INSTALLATION_ID=$(grep "INSTALLATION_ID:" "$ENV_FILE" | grep -v "#" | awk '{print $2}' | tr -d '"')
    REPO_OWNER=$(grep "REPO_OWNER:" "$ENV_FILE" | grep -v "#" | awk '{print $2}' | tr -d '"')
    REPO_NAME=$(grep "REPO_NAME:" "$ENV_FILE" | grep -v "#" | awk '{print $2}' | tr -d '"')
    
    echo "   GitHub App ID: $GITHUB_APP_ID"
    echo "   Installation ID: $INSTALLATION_ID"
    echo "   Repository: $REPO_OWNER/$REPO_NAME"
    
    if [ -z "$GITHUB_APP_ID" ] || [ -z "$REPO_OWNER" ] || [ -z "$REPO_NAME" ]; then
        echo -e "${RED}Missing required configuration${NC}"
    else
        echo -e "${GREEN}Configuration looks complete${NC}"
    fi
else
    echo -e "${RED}$ENV_FILE not found${NC}"
    echo "Create it: cp configs/env.yaml.production env-cloudrun.yaml"
fi

# Check Cloud Run deployment
echo ""
echo "Checking Cloud Run deployment..."
SERVICE_URL=$(gcloud run services describe github-copier --format="value(status.url)" 2>/dev/null)
if [ -n "$SERVICE_URL" ]; then
    echo -e "${GREEN}Cloud Run service exists${NC}"
    echo "   URL: $SERVICE_URL"
    
    # Try to hit health endpoint
    echo ""
    echo "Checking health endpoint..."
    if curl -s -f "$SERVICE_URL/health" &>/dev/null; then
        echo -e "${GREEN}Health endpoint responding${NC}"
        curl -s "$SERVICE_URL/health" | python3 -m json.tool 2>/dev/null || echo ""
    else
        echo -e "${RED}Health endpoint not responding${NC}"
    fi

    echo ""
    echo "Checking readiness endpoint..."
    if curl -s -f "$SERVICE_URL/ready" &>/dev/null; then
        echo -e "${GREEN}Readiness endpoint responding${NC}"
        curl -s "$SERVICE_URL/ready" | python3 -m json.tool 2>/dev/null || echo ""
    else
        echo -e "${YELLOW}Readiness endpoint returned non-200 (may indicate auth or connectivity issue)${NC}"
        curl -s "$SERVICE_URL/ready" | python3 -m json.tool 2>/dev/null || echo ""
    fi
else
    echo -e "${YELLOW}No Cloud Run service 'github-copier' found${NC}"
fi

# Summary
echo ""
echo "Summary & Next Steps"
echo "======================="
echo ""

# Check for common issues
ISSUES_FOUND=0

if ! gcloud secrets get-iam-policy CODE_COPIER_PEM --format="value(bindings.members)" 2>/dev/null | grep -q "$SERVICE_ACCOUNT"; then
    echo -e "${RED}Issue: Service account doesn't have access to CODE_COPIER_PEM${NC}"
    echo "   Fix: Run ./scripts/grant-secret-access.sh"
    ISSUES_FOUND=$((ISSUES_FOUND + 1))
fi

if [ ! -f "$ENV_FILE" ]; then
    echo -e "${RED}Issue: $ENV_FILE not found${NC}"
    echo "   Fix: cp configs/env.yaml.production env-cloudrun.yaml && nano env-cloudrun.yaml"
    ISSUES_FOUND=$((ISSUES_FOUND + 1))
fi

if [ $ISSUES_FOUND -eq 0 ]; then
    echo -e "${GREEN}No obvious issues found${NC}"
    echo ""
    echo "If you're still seeing 401 errors, check:"
    echo "1. GitHub App is installed on the repository: https://github.com/settings/installations"
    echo "2. Installation ID matches the repository"
    echo "3. Private key in Secret Manager matches the GitHub App"
    echo "4. GitHub App has 'Contents' write and 'Pull requests' write permissions"
    echo "   (see github-app-manifest.yml for required permissions)"
    echo ""
    echo "View logs: gcloud run services logs read github-copier --limit=50"
else
    echo ""
    echo -e "${YELLOW}Found $ISSUES_FOUND issue(s) - please fix them and try again${NC}"
fi
