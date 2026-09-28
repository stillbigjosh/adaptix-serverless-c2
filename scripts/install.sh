#!/bin/bash
set -e

export PATH="/usr/local/go/bin:$PATH"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

if [ -z "$1" ]; then
    echo "Usage: $0 /path/to/AdaptixC2"
    echo "  Installs the Serverless C2 channel (Lambda/DynamoDB) into an Adaptix C2 installation."
    exit 1
fi

ADAPTIX_DIR="$1"

if [ ! -d "$ADAPTIX_DIR/AdaptixServer" ]; then
    echo "Error: $ADAPTIX_DIR does not appear to be a valid Adaptix C2 directory."
    echo "Expected: $ADAPTIX_DIR/AdaptixServer/"
    exit 1
fi

DIST_DIR="$ADAPTIX_DIR/dist"

echo "[*] Installing Serverless C2 channel (Lambda/DynamoDB) for Adaptix..."

# --- Phase 1: Backup dist/ user files (certs, profile, database) ---

BACKUP_DIR="$ADAPTIX_DIR/.dist_backup_$$"
if [ -d "$DIST_DIR" ]; then
    echo "[+] Backing up dist/ user files..."
    mkdir -p "$BACKUP_DIR"
    for f in "$DIST_DIR"/*; do
        name="$(basename "$f")"
        [ "$name" = "extenders" ] && continue
        cp -a "$f" "$BACKUP_DIR/"
    done
    echo "    Saved to $BACKUP_DIR"
fi

# --- Phase 2: Apply source patches ---

BEACON_AGENT_DIR="$ADAPTIX_DIR/AdaptixServer/extenders/beacon_agent"
BEACON_SRC="$BEACON_AGENT_DIR/src_beacon/beacon"

echo "[+] Copying ConnectorServerless source files..."
if [ -f "$PROJECT_DIR/beacon/ConnectorServerless.h" ] && [ -d "$BEACON_SRC" ]; then
    cp "$PROJECT_DIR/beacon/ConnectorServerless.h"   "$BEACON_SRC/ConnectorServerless.h"
    cp "$PROJECT_DIR/beacon/ConnectorServerless.cpp" "$BEACON_SRC/ConnectorServerless.cpp"
    echo "    Copied to $BEACON_SRC/"
else
    echo "    [!] No beacon connector source found (Kharon-only mode)"
fi

apply_patch() {
    local patch_file="$1"
    local patch_name="$(basename "$patch_file")"

    if [ ! -f "$patch_file" ]; then
        echo "    Skipped (not found): $patch_name"
        return
    fi

    if patch --dry-run -p1 -d "$ADAPTIX_DIR" < "$patch_file" > /dev/null 2>&1; then
        patch -p1 -d "$ADAPTIX_DIR" < "$patch_file"
        echo "    Applied: $patch_name"
    else
        echo "    Skipped (already applied or conflict): $patch_name"
    fi
}

echo "[+] Applying patches..."
for pf in "$PROJECT_DIR"/patches/*.patch; do
    [ -f "$pf" ] && apply_patch "$pf"
done

# --- Phase 3: Register BeaconServerless in agent configs ---

echo "[+] Registering BeaconServerless in agent configs..."

# beacon_agent (source config.yaml used by make)
BA_CONFIG="$BEACON_AGENT_DIR/config.yaml"
if [ -f "$BA_CONFIG" ] && ! grep -q "BeaconServerless" "$BA_CONFIG"; then
    sed -i '/listeners:/a\  - "BeaconServerless"' "$BA_CONFIG"
    echo "    Added to beacon_agent source config"
elif [ -f "$BA_CONFIG" ]; then
    echo "    beacon_agent: already registered"
fi

# agent_kharon (source dir has no config.yaml; handled in Phase 6)

# --- Phase 4: Detect GOEXPERIMENT and build listener plugin ---

echo "[+] Building listener plugin..."
cd "$PROJECT_DIR/listener"

if [ -f "$DIST_DIR/adaptixserver" ]; then
    SERVER_GOFLAGS=$(go version "$DIST_DIR/adaptixserver" 2>/dev/null | grep -oP 'X:\K\S+' || true)
    if [ -n "$SERVER_GOFLAGS" ]; then
        export GOEXPERIMENT="$SERVER_GOFLAGS"
        echo "    Detected GOEXPERIMENT=$GOEXPERIMENT from server binary"
    fi
fi

go mod tidy
make build
echo "    Built successfully."

# --- Phase 5: Rebuild server + extenders ---

echo "[+] Rebuilding Adaptix server and extenders..."
cd "$ADAPTIX_DIR"

# make server-ext runs clean (wipes dist/), then server, then extenders.
# The extenders target needs dist/extenders/ to exist.
make server
mkdir -p "$DIST_DIR/extenders"
make extenders

# --- Phase 6: Restore dist/ state ---

echo "[+] Restoring user files from backup..."
if [ -d "$BACKUP_DIR" ]; then
    for f in "$BACKUP_DIR"/*; do
        name="$(basename "$f")"
        cp -a "$f" "$DIST_DIR/"
    done
    rm -rf "$BACKUP_DIR"
    echo "    Restored certs, profile, database, and custom files"
fi

# --- Phase 6b: Ensure SSL certs exist ---

CERT_FILE=$(grep -oP '^\s*cert:\s*"\K[^"]+' "$DIST_DIR/profile.yaml" 2>/dev/null || echo "server.rsa.crt")
KEY_FILE=$(grep -oP '^\s*key:\s*"\K[^"]+' "$DIST_DIR/profile.yaml" 2>/dev/null || echo "server.rsa.key")

if [ ! -f "$DIST_DIR/$CERT_FILE" ] || [ ! -f "$DIST_DIR/$KEY_FILE" ]; then
    echo "[+] SSL certs missing, generating self-signed..."
    openssl req -x509 -newkey rsa:4096 \
        -keyout "$DIST_DIR/$KEY_FILE" \
        -out "$DIST_DIR/$CERT_FILE" \
        -days 365 -nodes -subj "/CN=localhost" 2>/dev/null
    echo "    Generated $CERT_FILE and $KEY_FILE"
fi

# --- Phase 6c: Create agent source symlinks in dist/ ---
# The agent builder needs src_beacon, src_loader, src_core at runtime
# to compile payloads. These are in the source tree, not in dist/.

echo "[+] Creating agent source symlinks in dist/..."
for agent_name in beacon_agent agent_kharon; do
    SRC_EXT="$ADAPTIX_DIR/AdaptixServer/extenders/$agent_name"
    DIST_EXT="$DIST_DIR/extenders/$agent_name"
    if [ -d "$SRC_EXT" ] && [ -d "$DIST_EXT" ]; then
        for subdir in src_beacon src_loader src_core; do
            if [ -d "$SRC_EXT/$subdir" ] && [ ! -e "$DIST_EXT/$subdir" ]; then
                ln -s "$SRC_EXT/$subdir" "$DIST_EXT/$subdir"
                echo "    Linked $agent_name/$subdir"
            fi
        done
    fi
done

# --- Phase 6d: Ensure Kharon extender configs and AX scripts exist ---
# Kharon extender Makefiles only produce the .so, they don't copy
# config.yaml or ax_config.axs to dist/. We ship these in the project.

echo "[+] Installing Kharon extender configs..."

KHARON_AGENT_DIST="$DIST_DIR/extenders/agent_kharon"
if [ -d "$KHARON_AGENT_DIST" ]; then
    if [ ! -f "$KHARON_AGENT_DIST/config.yaml" ]; then
        cp "$PROJECT_DIR/kharon/agent_kharon/config.yaml" "$KHARON_AGENT_DIST/"
        echo "    Installed agent_kharon/config.yaml"
    else
        if ! grep -q "BeaconServerless" "$KHARON_AGENT_DIST/config.yaml"; then
            sed -i '/listeners:/a\  - "BeaconServerless"' "$KHARON_AGENT_DIST/config.yaml"
            echo "    Added BeaconServerless to agent_kharon/config.yaml"
        fi
    fi
    if [ ! -f "$KHARON_AGENT_DIST/ax_config.axs" ]; then
        cp "$PROJECT_DIR/kharon/agent_kharon/ax_config.axs" "$KHARON_AGENT_DIST/"
        echo "    Installed agent_kharon/ax_config.axs"
    fi
fi

KHARON_HTTP_DIST="$DIST_DIR/extenders/listener_kharon_http"
if [ -d "$KHARON_HTTP_DIST" ]; then
    if [ ! -f "$KHARON_HTTP_DIST/config.yaml" ]; then
        cp "$PROJECT_DIR/kharon/listener_kharon_http/config.yaml" "$KHARON_HTTP_DIST/"
        echo "    Installed listener_kharon_http/config.yaml"
    fi
    if [ ! -f "$KHARON_HTTP_DIST/ax_config.axs" ]; then
        cp "$PROJECT_DIR/kharon/listener_kharon_http/ax_config.axs" "$KHARON_HTTP_DIST/"
        echo "    Installed listener_kharon_http/ax_config.axs"
    fi
fi

# --- Phase 7: Install serverless listener plugin ---

echo "[+] Installing serverless listener plugin..."
PLUGIN_DIR="$DIST_DIR/extenders/beacon_listener_serverless"
mkdir -p "$PLUGIN_DIR"
cp "$PROJECT_DIR/listener/beacon_listener_serverless.so" "$PLUGIN_DIR/"
cp "$PROJECT_DIR/listener/config.yaml" "$PLUGIN_DIR/"
cp "$PROJECT_DIR/listener/ax_config.axs" "$PLUGIN_DIR/"

if ! grep -q "beacon_listener_serverless" "$DIST_DIR/profile.yaml" 2>/dev/null; then
    if grep -q "beacon_listener_dns" "$DIST_DIR/profile.yaml" 2>/dev/null; then
        sed -i '/beacon_listener_dns/a\    - "extenders/beacon_listener_serverless/config.yaml"' "$DIST_DIR/profile.yaml"
    elif grep -q "listener_kharon_http" "$DIST_DIR/profile.yaml" 2>/dev/null; then
        sed -i '/listener_kharon_http/a\    - "extenders/beacon_listener_serverless/config.yaml"' "$DIST_DIR/profile.yaml"
    elif grep -q "agent_kharon" "$DIST_DIR/profile.yaml" 2>/dev/null; then
        sed -i '/agent_kharon/a\    - "extenders/beacon_listener_serverless/config.yaml"' "$DIST_DIR/profile.yaml"
    else
        echo "    [!] Could not find extenders list in profile.yaml. Add manually:"
        echo '        - "extenders/beacon_listener_serverless/config.yaml"'
    fi
fi
echo "    Installed to $PLUGIN_DIR/"

# --- Phase 7b: Build objects_serverless ---

echo "[+] Building objects_serverless..."
if [ -f "$SCRIPT_DIR/build_objects.sh" ]; then
    bash "$SCRIPT_DIR/build_objects.sh" "$ADAPTIX_DIR"
else
    echo "    [!] build_objects.sh not found, skipping beacon object build"
fi

# --- Phase 8: Create systemd service if missing ---

if command -v systemctl > /dev/null 2>&1 && [ ! -f /etc/systemd/system/adaptix.service ]; then
    echo "[+] Creating systemd service..."
    cat > /etc/systemd/system/adaptix.service <<SVCEOF
[Unit]
Description=AdaptixC2 Teamserver
After=network.target

[Service]
Type=simple
Environment=HOME=/root
WorkingDirectory=$DIST_DIR
ExecStart=$DIST_DIR/adaptixserver -profile profile.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
SVCEOF
    systemctl daemon-reload
    systemctl enable adaptix
    echo "    Created and enabled adaptix.service"
else
    echo "[+] Systemd service already exists or systemd not available"
fi

# --- Phase 9: Restart service ---

echo "[+] Restarting Adaptix service..."
if systemctl is-active --quiet adaptix 2>/dev/null; then
    systemctl restart adaptix
    sleep 2
    if systemctl is-active --quiet adaptix 2>/dev/null; then
        echo "    Adaptix restarted successfully"
    else
        echo "    Warning: Adaptix failed to start. Check: journalctl -u adaptix"
    fi
elif command -v systemctl > /dev/null 2>&1; then
    systemctl start adaptix
    sleep 2
    if systemctl is-active --quiet adaptix 2>/dev/null; then
        echo "    Adaptix started"
    else
        echo "    Warning: Adaptix failed to start. Check: journalctl -u adaptix"
    fi
else
    echo "    No systemd available. Restart the server manually."
fi

echo ""
echo "[*] Installation complete!"
echo "    Create a 'BeaconServerless' listener in the Adaptix GUI."
echo "    Deploy AWS infrastructure first: cd deploy/aws && terraform apply"
