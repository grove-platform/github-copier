// test-pem verifies a GitHub App PEM private key by generating a JWT
// and calling the GitHub API's /app endpoint. This confirms the key
// is valid, correctly formatted, and matches the App ID.
package main

import (
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, `test-pem — verify a GitHub App PEM key

Usage: test-pem <pem-file> <app-id>

Arguments:
  pem-file  Path to the .pem private key file
  app-id    GitHub App ID (numeric)

Example:
  test-pem github-app.pem 123456
`)
		os.Exit(1)
	}

	pemPath := os.Args[1]
	appID := os.Args[2]

	pemData, err := os.ReadFile(pemPath) // #nosec G304 -- CLI tool, path from user arg
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to read PEM file %q: %v\n", pemPath, err)
		os.Exit(1)
	}
	fmt.Printf("✓ Read PEM file: %s (%d bytes)\n", pemPath, len(pemData))

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(pemData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to parse RSA private key: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Ensure the file is a valid PKCS#1 or PKCS#8 PEM-encoded RSA key.\n")
		os.Exit(1)
	}
	fmt.Printf("✓ Parsed RSA private key (size: %d bits)\n", privateKey.N.BitLen())

	token, err := generateJWT(appID, privateKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to generate JWT: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Generated JWT for App ID %s\n", appID)

	req, err := http.NewRequest("GET", "https://api.github.com/app", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	fmt.Println("\nContacting GitHub API...")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ API request failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to read response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Status: %d\n", resp.StatusCode)
	if resp.StatusCode == http.StatusOK {
		fmt.Printf("✅ Authentication successful!\n\nApp info:\n%s\n", body)
	} else {
		fmt.Fprintf(os.Stderr, "❌ Authentication failed (HTTP %d)\n%s\n", resp.StatusCode, body)
		os.Exit(1)
	}
}

func generateJWT(appID string, pk *rsa.PrivateKey) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(pk)
}
