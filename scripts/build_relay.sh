#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
LAMBDA_DIR="$PROJECT_DIR/deploy/aws/lambda"

echo "[*] Building Lambda relay binary..."

cd "$LAMBDA_DIR"

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bootstrap main.go
zip -j "$PROJECT_DIR/deploy/aws/lambda.zip" bootstrap
rm bootstrap

echo "[+] Built: deploy/aws/lambda.zip"
echo "    Deploy with: cd deploy/aws && terraform apply"
