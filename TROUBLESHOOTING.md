# Troubleshooting Guide

## Agent Not Checking In

### 1. Verify Lambda is receiving requests

Check CloudWatch logs for the Lambda function:

```bash
aws logs filter-log-events \
  --log-group-name /aws/lambda/adaptix-relay \
  --start-time $(date -d '5 minutes ago' +%s000) \
  --region us-east-1
```

- If no events: the agent can't reach the Lambda URL (DNS, firewall, proxy)
- If events show 1-2ms duration with no app logs: requests are hitting "empty body" (see GET handling below)
- If events show errors: check the error message

### 2. Verify DynamoDB inbound records

```bash
aws dynamodb scan --table-name adaptix-inbound --region us-east-1 --select COUNT
```

If count is 0 but Lambda is getting requests, the Lambda can't write to DynamoDB (IAM permissions).

### 3. Verify the listener is polling

Check adaptix service logs:

```bash
journalctl -u adaptix --no-pager -n 50 | grep serverless
```

You should see: `transport started, polling adaptix-inbound every 5s`

If you see `transport stopped` immediately after start, the listener failed to initialize (check AWS credentials on the server).

### 4. Check for "empty body" errors

If Lambda logs show very short durations (1-2ms) with no application-level log lines, requests are returning the "empty body" error response. This happens when:

- **GET requests aren't handled**: The Lambda must extract data from `QueryStringParameters["id"]` for GET requests. Kharon randomly alternates GET/POST.
- **Profile mismatch**: The malleable profile's `client_parameters` must include `{"id": ""}` for both GET and POST methods.

**Fix**: Ensure the deployed Lambda has the GET query parameter extraction code.

## Agent Registers But Stays "Unconnected"

### Symptoms
- Logs show: `heartbeat from unconnected agent <id> (key in tail, len=44)`
- Agent keeps sending 44-byte packets indefinitely

### Root cause: Registration response not delivered during Checkin

The async Lambda/DynamoDB model means the registration response may not be ready when the agent first checks in. The Lambda must poll the outbound table for several seconds.

**Check**: Look at the outbound table:
```bash
aws dynamodb scan --table-name adaptix-outbound --region us-east-1 \
  --projection-expression "agent_id,delivered"
```

- If `delivered: false`: Lambda never picked up the response (poll loop too short, or Lambda timeout too low)
- If `delivered: true` but agent still unconnected: the response was delivered during GetTask, not Checkin. The Lambda poll loop needs to be long enough (8 seconds) and the Lambda timeout must be >= 15 seconds.

**Fix**: Verify Lambda timeout is at least 30 seconds in Terraform (`timeout = 30`). Verify the Lambda's poll loop iterates 8 times with 1-second sleeps.

## Agent Registers and Connects But Tasks Don't Execute

### Symptoms
- Logs show: `queued N bytes response for agent <id>`
- Agent keeps polling with 44-byte GetTask packets
- No task result comes back

### Root cause: Wrong UUID prefix in response

The response must be prefixed with the UUID from the agent's CURRENT request, not the original registration UUID. After the agent becomes Connected, it uses a new UUID assigned during registration.

**Check**: In `deliverQueuedTasks`, the response must use the `wireUUID` parameter (extracted from the current `raw` packet), not `state.oldAgentID`.

### Symptoms: "unknown task action 0xNN"
- The decrypted data's first byte doesn't match any known action (0x00=GetTask, 0x01=PostTask, 0x05=QuickMsg, 0x07=QuickOut)
- Usually means decryption failed (wrong key or wrong data boundaries)

**Check the key**: The encryption key is the 16 bytes extracted from the initial registration packet. Verify it matches between the listener's `agentState.encryptKey` and what the agent sent.

**Check the wire format**: If the agent is !Connected, the 44-byte packet has key stomping. The listener must detect this (tail bytes match stored key) and NOT try to decrypt the stomped data.

## "Command not found" in the GUI

### Symptoms
- Agent appears in the GUI, but typing commands returns `[-] Command not found`

### Root cause: AXS command registration missing for BeaconServerless

The Kharon agent's `ax_config.axs` registers commands only for known listener types. If `BeaconServerless` is not in the condition, no commands are available.

**Fix**: In `kharon/agent_kharon/ax_config.axs`, ensure the listener type check includes BeaconServerless:
```javascript
if(listenerType == "KharonHTTP" || listenerType == "BeaconServerless") {
```

Then copy the updated file to:
```
/opt/AdaptixC2/dist/extenders/agent_kharon/ax_config.axs
```
And restart the service.

## Keys Lost After Service Restart

### Symptoms
- Agent was working before restart
- After restart: `unknown task action` errors or agent not recognized

### Root cause: In-memory keys not persisted

Agent encryption keys must be saved via `TsExtenderDataSave` during registration and recovered via `TsExtenderDataLoad` on restart.

**Check**: Query the ExtenderData table:
```bash
sqlite3 /opt/AdaptixC2/dist/data/adaptixserver.db \
  "SELECT Key FROM ExtenderData WHERE Name LIKE '%key_%';"
```

If empty, the key persistence code is not running. Verify `TsExtenderData*` methods are in the `Teamserver` interface in `pl_main.go`.

## GOEXPERIMENT Mismatch

### Symptoms
- Plugin fails to load: `plugin was built with a different version of package` or similar

### Root cause
The listener plugin must be built with the same `GOEXPERIMENT` flags as the AdaptixC2 server binary.

**Check**:
```bash
go version /opt/AdaptixC2/dist/adaptixserver
```

Look for `X:jsonv2,greenteagc` or similar. Set the same flags when building:
```bash
export GOEXPERIMENT=jsonv2,greenteagc
go build -buildmode=plugin -o beacon_listener_serverless.so .
```

## DynamoDB Cleanup

Purge stale records from both tables:

```python
# purge_dynamo.py - run on the C2 server
import json, subprocess, sys

TABLE = sys.argv[1] if len(sys.argv) > 1 else "adaptix-inbound"
REGION = "us-east-1"

def aws(*args):
    result = subprocess.run(["aws"] + list(args), capture_output=True, text=True)
    return json.loads(result.stdout) if result.stdout else {}

items = []
resp = aws("dynamodb", "scan", "--table-name", TABLE,
           "--projection-expression", "id", "--region", REGION)
items.extend(resp.get("Items", []))

for i in range(0, len(items), 25):
    batch = items[i:i+25]
    requests = [{"DeleteRequest": {"Key": item}} for item in batch]
    payload = json.dumps({TABLE: requests})
    aws("dynamodb", "batch-write-item", "--request-items", payload, "--region", REGION)

print(f"Purged {len(items)} records from {TABLE}")
```

Usage:
```bash
python3 purge_dynamo.py adaptix-inbound
python3 purge_dynamo.py adaptix-outbound
```

## Useful Debug Commands

```bash
# Service logs (filtered for serverless listener)
journalctl -u adaptix --no-pager -f | grep serverless

# DynamoDB record counts
aws dynamodb scan --table-name adaptix-inbound --region us-east-1 --select COUNT
aws dynamodb scan --table-name adaptix-outbound --region us-east-1 --select COUNT

# Lambda recent invocations
aws logs filter-log-events --log-group-name /aws/lambda/adaptix-relay \
  --start-time $(date -d '5 minutes ago' +%s000) --region us-east-1

# Check Lambda timeout
aws lambda get-function-configuration --function-name adaptix-relay \
  --region us-east-1 --query 'Timeout'

# Check agent in database
sqlite3 /opt/AdaptixC2/dist/data/adaptixserver.db \
  "SELECT Id, Computer, Username, Sleep FROM Agents;"

# Check persisted keys
sqlite3 /opt/AdaptixC2/dist/data/adaptixserver.db \
  "SELECT Name, Key FROM ExtenderData;"
```
