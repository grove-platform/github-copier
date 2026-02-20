# test-pem

Verify a GitHub App PEM private key by generating a JWT and calling the GitHub `/app` endpoint.

## Purpose

Quickly confirm that:

- The PEM file is valid and correctly formatted (PKCS#1 or PKCS#8)
- The App ID matches the key
- GitHub accepts the resulting JWT

## Build

```bash
go build -o test-pem ./cmd/test-pem
```

## Usage

```bash
./test-pem <pem-file> <app-id>
```

**Arguments:**

| Argument  | Description                        |
|-----------|------------------------------------|
| pem-file  | Path to the `.pem` private key     |
| app-id    | GitHub App ID (numeric)            |

## Example

```bash
$ ./test-pem github-app.pem 123456
✓ Read PEM file: github-app.pem (1674 bytes)
✓ Parsed RSA private key (size: 2048 bits)
✓ Generated JWT for App ID 123456

Contacting GitHub API...
Status: 200
✅ Authentication successful!

App info:
{"id":123456,"slug":"my-app","name":"My App",...}
```

## Troubleshooting

| Error | Cause | Fix |
|-------|-------|-----|
| `Failed to read PEM file` | File not found or unreadable | Check path and permissions |
| `Failed to parse RSA private key` | Not a valid PEM key | Re-download from GitHub App settings |
| `HTTP 401` | App ID doesn't match key, or key is revoked | Verify App ID; regenerate key if needed |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0    | Authentication succeeded |
| 1    | Any error (file, parse, network, auth) |
