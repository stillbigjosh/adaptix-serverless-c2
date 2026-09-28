# Adaptix Serverless C2

Serverless C2 transport plugin for [AdaptixC2 v1.2](https://github.com/Adaptix-Framework/AdaptixC2) using AWS Lambda + DynamoDB as the relay infrastructure. Agent traffic appears as outbound HTTPS to AWS endpoints, requiring no inbound ports or public IPs on the C2 server.

## Architecture

```
Kharon Agent (target)
    |
    | HTTPS GET/POST (outbound only, randomly alternated)
    v
AWS Lambda Function URL (stateless relay)
    |
    | Store inbound / Poll outbound (up to 8s)
    v
DynamoDB (inbound + outbound tables)
    ^
    | Poll every 5 seconds
    |
Listener Plugin (inside AdaptixC2)
    |
    | TsAgent API + TsExtenderData (key persistence)
    v
AdaptixC2 Teamserver + UI
```

**Data flow:**
1. [Kharon](https://github.com/entropy-z/Kharon) agent sends encrypted check-in via HTTPS to Lambda Function URL
2. Lambda stores raw data in DynamoDB `inbound` table
3. Lambda polls `outbound` table for up to 8 seconds (bridges the async gap for registration)
4. If a response exists, Lambda returns it and marks it delivered
5. AdaptixC2 listener plugin polls DynamoDB `inbound` table for unprocessed records
6. Listener parses Kharon wire protocol (LokyCrypt encrypted), registers/updates agent
7. When operator issues commands, listener encrypts them in Kharon format, stores in `outbound`
8. On next check-in, Lambda returns the response to the agent

## Components

| Component | Path | Purpose |
|-----------|------|---------|
| Kharon agent | `agent/` | Bundled Kharon implant (C++ source) |
| Terraform | `deploy/aws/` | Lambda + DynamoDB + IAM + KMS infrastructure |
| Lambda relay | `deploy/aws/lambda/` | Stateless HTTP-to-DynamoDB proxy with outbound polling |
| Listener plugin | `listener/` | AdaptixC2 plugin, DynamoDB polling, Kharon protocol bridge |
| Kharon configs | `kharon/` | AXS command registration and config for agent/listener extenders |
| Patches | `patches/` | AdaptixC2 source patches for BeaconServerless support |
| Profiles | `profiles/` | Malleable HTTP profile for Lambda URL |
| Scripts | `scripts/` | Install, build, deploy, uninstall |

## Prerequisites

- AWS account with credentials configured (`aws configure`)
- Terraform >= 1.5.0
- Go >= 1.23 (listener plugin requires matching GOEXPERIMENT with server binary)
- AdaptixC2 v1.2 installed
- clang++ with mingw targets: `apt install clang lld`
- NASM assembler: `apt install nasm`
- objcopy: `apt install binutils-mingw-w64-x86-64`

## Installation

### Step 1: Deploy AWS Infrastructure

```bash
# Build the Lambda relay binary (cross-compiled for Amazon Linux)
./scripts/build_relay.sh

# Deploy Lambda + DynamoDB + IAM with Terraform
./scripts/deploy_infra.sh

# Note the Lambda Function URL from the output, e.g.:
# lambda_function_url = "https://xxxxx.lambda-url.us-east-1.on.aws"
```

### Step 2: Install Plugin into AdaptixC2

```bash
./scripts/install.sh /path/to/AdaptixC2
```

This script will:
1. Back up your `dist/` directory (certs, database, profile)
2. Apply source patches for BeaconServerless connector support
3. Detect `GOEXPERIMENT` flags from the server binary
4. Build the listener plugin (`.so`) with matching flags
5. Rebuild AdaptixC2 server and extenders
6. Restore your backed-up files (certs, database, profile)
7. Install the serverless listener plugin + Kharon extender configs
8. Create/update the systemd service
9. Restart AdaptixC2

### Step 3: Configure Listener in the GUI

1. Log into the AdaptixC2 web UI
2. Go to Listeners and create a new `BeaconServerless` listener
3. Set **Lambda URL** to the Function URL from Step 1
4. Set **Relay API Key** (must match `relay_api_key` in your terraform.tfvars)
5. Optionally adjust: AWS Region, Poll Interval (default 5s), TTL Hours (default 24)
6. Upload the malleable profile from `profiles/lambda_default.json`
7. Start the listener

### Step 4: Generate and Execute Payload

1. In the GUI, generate a Kharon agent selecting the `BeaconServerless` listener
2. Choose format (Exe, Dll, Svc) and architecture (x64)
3. Execute the payload on the target
4. The agent should check in within seconds

## Configuration

### Terraform Variables (`deploy/aws/terraform.tfvars`)

```hcl
region        = "us-east-1"
function_name = "adaptix-relay"
relay_api_key = "your-secret-key-here"
tags = {
  Project = "adaptix-serverless"
}
```

### Listener Settings (AdaptixC2 GUI)

| Field | Description | Default |
|-------|-------------|---------|
| AWS Region | Region where infra is deployed | us-east-1 |
| Lambda URL | Function URL from Terraform output | (required) |
| Relay API Key | Must match Terraform's relay_api_key | (optional) |
| Inbound Table | DynamoDB table for agent check-ins | adaptix-inbound |
| Outbound Table | DynamoDB table for server responses | adaptix-outbound |
| Poll Interval | How often to check DynamoDB (seconds) | 5 |
| TTL Hours | DynamoDB record expiry | 24 |

## Key Technical Details

### Kharon Wire Protocol

The Kharon agent uses a custom binary protocol over HTTP:

- **New agent (registration):** `[36-byte UUID][encrypted_checkin_data][16-byte LokyCrypt key]`
- **Connected agent (GetTask):** `[36-byte UUID][encrypted_payload]` (no trailing key)
- **!Connected agent (key stomping):** When payload is small, the 16-byte key at `TotalLen-16` overlaps into the UUID area. For a 44-byte packet, key starts at offset 28, stomping UUID bytes 28-35 and all encrypted data.

The listener detects all three formats and handles them correctly.

### Lambda Outbound Poll Loop

The async nature of Lambda + DynamoDB means the listener hasn't processed the inbound record when Lambda first checks for a response. The Lambda polls the outbound table for up to 8 seconds after storing an inbound record, giving the listener time to process and queue the response. This is critical for registration (the Checkin response must arrive during the Checkin phase, not a later GetTask).

### Key Persistence

Agent encryption keys are persisted via `TsExtenderDataSave`/`TsExtenderDataLoad` (SQLite-backed). This survives service restarts. On restart, the listener recovers keys from the persistent store when it encounters a known agent ID.

### GET/POST Handling

Kharon randomly alternates between GET and POST requests (`HTTP_METHOD_USE_BOTH`). GET requests put data in the `id` query parameter (base64-encoded per the profile). POST requests put raw binary in the HTTP body. The Lambda handles both transparently.

## OPSEC

- All agent traffic is outbound HTTPS to `*.lambda-url.*.on.aws` endpoints
- No inbound ports required on the C2 server
- DynamoDB records auto-expire via TTL
- CloudWatch logs are KMS-encrypted with 3-day retention
- Lambda relay authenticates via API key header
- Consider using a custom domain with CloudFront for domain fronting
- The Lambda poll loop adds 0-8 seconds latency to responses

## Uninstall

```bash
# Remove plugin from AdaptixC2
./scripts/uninstall.sh /path/to/AdaptixC2

# Destroy AWS infrastructure
cd deploy/aws && terraform destroy
```

## Project Structure

```
adaptix-serverless-c2/
  agent/
    src_beacon/              # Kharon implant (C++ source)
    src_loader/              # Loader wrappers (EXE, DLL, SVC)
  deploy/
    aws/
      main.tf                # Lambda + DynamoDB + IAM + KMS
      variables.tf           # Terraform variables
      outputs.tf             # Output Lambda URL and table ARNs
      lambda/
        main.go              # Lambda relay handler
  listener/
    pl_main.go               # AdaptixC2 plugin entry (Teamserver interface)
    pl_transport.go          # DynamoDB polling, Kharon protocol, LokyCrypt
    ax_config.axs            # Listener creation UI
    config.yaml              # Plugin metadata
  kharon/
    agent_kharon/            # AXS commands + config for Kharon agent extender
    listener_kharon_http/    # AXS + config for Kharon HTTP listener extender
  patches/                   # AdaptixC2 source patches
  profiles/
    lambda_default.json      # Malleable profile for Lambda URL
  scripts/
    install.sh               # Full install into AdaptixC2
    uninstall.sh             # Reverse patches + cleanup
    build_relay.sh           # Cross-compile Lambda binary
    build_agent.sh           # Build Kharon agent shellcode
    build_objects.sh         # Build beacon objects
    deploy_infra.sh          # Terraform init + apply
```
