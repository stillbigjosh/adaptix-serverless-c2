#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

if [ -z "$1" ]; then
    echo "Usage: $0 /path/to/AdaptixC2"
    echo "  Removes the Serverless C2 channel from an Adaptix C2 installation."
    exit 1
fi

ADAPTIX_DIR="$1"
AGENT_DIR="$ADAPTIX_DIR/AdaptixServer/extenders/beacon_agent"
BEACON_SRC="$AGENT_DIR/src_beacon/beacon"
DIST_DIR="$ADAPTIX_DIR/dist"

echo "[*] Uninstalling Serverless C2 channel..."

# Reverse patches
echo "[+] Reversing patches..."

reverse_patch() {
    local patch_file="$1"
    local patch_name="$(basename "$patch_file")"

    if patch --dry-run -R -p1 -d "$ADAPTIX_DIR" < "$patch_file" > /dev/null 2>&1; then
        patch -R -p1 -d "$ADAPTIX_DIR" < "$patch_file"
        echo "    Reversed: $patch_name"
    else
        echo "    Skipped (not applied or conflict): $patch_name"
    fi
}

reverse_patch "$PROJECT_DIR/patches/agent_build_payload_serverless.patch"
reverse_patch "$PROJECT_DIR/patches/agent_generate_profiles_serverless.patch"
reverse_patch "$PROJECT_DIR/patches/ax_config_serverless_commands.patch"
reverse_patch "$PROJECT_DIR/patches/agent_beat_serverless.patch"
reverse_patch "$PROJECT_DIR/patches/config_cpp_serverless.patch"
reverse_patch "$PROJECT_DIR/patches/agent_config_cpp_serverless.patch"
reverse_patch "$PROJECT_DIR/patches/agent_config_h_serverless.patch"
reverse_patch "$PROJECT_DIR/patches/agent_main_connector_serverless.patch"

# Remove connector source files
echo "[+] Removing ConnectorServerless source files..."
rm -f "$BEACON_SRC/ConnectorServerless.h"
rm -f "$BEACON_SRC/ConnectorServerless.cpp"

# Remove objects_serverless
echo "[+] Removing objects_serverless..."
rm -rf "$DIST_DIR/extenders/beacon_agent/objects_serverless"

# Remove BeaconServerless from config
echo "[+] Removing BeaconServerless from config..."
AGENT_CONFIG="$AGENT_DIR/config.yaml"
if [ -f "$AGENT_CONFIG" ]; then
    sed -i '/"BeaconServerless"/d' "$AGENT_CONFIG"
fi

# Remove listener plugin
echo "[+] Removing listener plugin..."
rm -rf "$DIST_DIR/extenders/beacon_listener_serverless"

# Remove from profile.yaml
if [ -f "$DIST_DIR/profile.yaml" ]; then
    sed -i '/beacon_listener_serverless/d' "$DIST_DIR/profile.yaml"
fi

echo ""
echo "[*] Uninstall complete. Rebuild the server: cd $ADAPTIX_DIR && make server-ext"
echo "    To destroy AWS infrastructure: cd deploy/aws && terraform destroy"
