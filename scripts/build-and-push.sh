#!/bin/bash
set -e

# Build and push Docker image to Artifactory
# Usage: ./scripts/build-and-push.sh [version]

VERSION=${1:-$(git describe --tags --always)}
IMAGE_REPO="795250896452.dkr.ecr.us-east-1.amazonaws.com/docs/github-copier"
IMAGE_TAG="${IMAGE_REPO}:${VERSION}"
IMAGE_LATEST="${IMAGE_REPO}:latest"

echo "🔨 Building Docker image..."
echo "   Version: ${VERSION}"
echo "   Image: ${IMAGE_TAG}"

# Build the image
docker build \
  --build-arg VERSION="${VERSION}" \
  -t "${IMAGE_TAG}" \
  -t "${IMAGE_LATEST}" \
  .

echo ""
echo "✅ Build complete!"
echo ""
echo "📦 Pushing to Artifactory..."

# Check if logged in to ECR
if ! docker info 2>/dev/null | grep -q "795250896452.dkr.ecr.us-east-1.amazonaws.com"; then
  echo "⚠️  Not logged in to ECR. Attempting login..."
  echo "   You may need AWS credentials (ecr_access_key/ecr_secret_key from Drone secrets)."
  echo "   For manual push, you can use: aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 795250896452.dkr.ecr.us-east-1.amazonaws.com"
  # Note: This will likely fail without AWS credentials
  # For CI/CD, Drone will handle authentication via kaniko plugin
fi

# Push both tags
docker push "${IMAGE_TAG}"
docker push "${IMAGE_LATEST}"

echo ""
echo "✅ Push complete!"
echo ""
echo "📋 Image details:"
echo "   Tagged: ${IMAGE_TAG}"
echo "   Latest: ${IMAGE_LATEST}"
echo ""
echo "🚀 Ready to deploy to Kanopy!"
echo ""
echo "Next steps:"
echo "  1. Create secrets: kubectl apply -f kanopy/staging/secrets.yaml"
echo "  2. Deploy with Helm (see kanopy/staging/README.md)"

