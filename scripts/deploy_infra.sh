#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
AWS_DIR="$PROJECT_DIR/deploy/aws"

echo "[*] Deploying AWS serverless infrastructure..."

# Build the Lambda relay first
bash "$SCRIPT_DIR/build_relay.sh"

cd "$AWS_DIR"

# Initialize Terraform if needed
if [ ! -d ".terraform" ]; then
    echo "[+] Initializing Terraform..."
    terraform init
fi

# Plan and apply
echo "[+] Planning deployment..."
terraform plan -out=tfplan

echo ""
read -p "[?] Apply this plan? (y/N) " -n 1 -r
echo ""

if [[ $REPLY =~ ^[Yy]$ ]]; then
    terraform apply tfplan
    rm -f tfplan

    echo ""
    echo "[+] Infrastructure deployed."
    echo "    Lambda URL: $(terraform output -raw relay_url)"
    echo ""
    echo "    Use this URL in the BeaconServerless listener config."
else
    rm -f tfplan
    echo "[!] Deployment cancelled."
fi
