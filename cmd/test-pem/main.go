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
		fmt.Println("Usage: test-pem <pem-file> <app-id>")
		os.Exit(1)
	}
	pemData, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("Read error:", err)
		os.Exit(1)
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(pemData)
	if err != nil {
		fmt.Println("Parse error:", err)
		os.Exit(1)
	}
	token, _ := generateJWT(os.Args[2], privateKey)
	req, _ := http.NewRequest("GET", "https://api.github.com/app", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %d\nBody: %s\n", resp.StatusCode, body)
}

func generateJWT(appID string, pk *rsa.PrivateKey) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "iss": appID}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(pk)
}
