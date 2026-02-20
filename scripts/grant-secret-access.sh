#!/bin/bash
# Grant Cloud Run service account access to all secrets
#
# NOTE: If you use a custom service account for Cloud Run (recommended),
# update SERVICE_ACCOUNT below to match. The default Compute Engine SA
# is used here as a baseline.

set -e

PROJECT_ID="github-copy-code-examples"
PROJECT_NUMBER="1054147886816"

# Cloud Run uses the default Compute Engine service account unless a custom SA is configured.
# Previously this script incorrectly targeted the App Engine SA (${PROJECT_NUMBER}@appspot.gserviceaccount.com).
SERVICE_ACCOUNT="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

echo "Granting Cloud Run service account access to secrets..."
echo "Service Account: ${SERVICE_ACCOUNT}"
echo ""

# Array of secrets to grant access to
SECRETS=(
  "CODE_COPIER_PEM"
  "webhook-secret"
  "mongo-uri"
)

for SECRET in "${SECRETS[@]}"; do
  echo "Granting access to: ${SECRET}"
  gcloud secrets add-iam-policy-binding "${SECRET}" \
    --member="serviceAccount:${SERVICE_ACCOUNT}" \
    --role="roles/secretmanager.secretAccessor" \
    --project="${PROJECT_ID}" 2>&1 | grep -E "Updated|bindings" || echo "  Already has access"
  echo ""
done

echo "Done! Verifying permissions..."
echo ""

for SECRET in "${SECRETS[@]}"; do
  echo "Permissions for ${SECRET}:"
  gcloud secrets get-iam-policy "${SECRET}" \
    --project="${PROJECT_ID}" \
    --format="table(bindings.members)" 2>&1 | grep -A 5 "serviceAccount:${SERVICE_ACCOUNT}" || echo "  Not found"
  echo ""
done

echo "All secrets are now accessible by Cloud Run!"
