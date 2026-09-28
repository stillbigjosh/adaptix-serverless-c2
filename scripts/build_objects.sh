#!/bin/bash
set -e

if [ -z "$1" ]; then
    echo "Usage: $0 /path/to/AdaptixC2"
    exit 1
fi

ADAPTIX_DIR="$1"
AGENT_DIR="$ADAPTIX_DIR/AdaptixServer/extenders/beacon_agent"
BEACON_SRC="$AGENT_DIR/src_beacon/beacon"
DIST_DIR="$ADAPTIX_DIR/dist"
OBJECTS_DIR="$DIST_DIR/extenders/beacon_agent/objects_serverless"

echo "[*] Building beacon objects with -DBEACON_SERVERLESS..."

mkdir -p "$OBJECTS_DIR/x64"
mkdir -p "$OBJECTS_DIR/x86"

COMPILER_X64="x86_64-w64-mingw32-g++"
COMPILER_X86="i686-w64-mingw32-g++"

if ! command -v "$COMPILER_X64" > /dev/null 2>&1; then
    echo "[!] MinGW cross-compiler not found: $COMPILER_X64"
    echo "    Install: apt install g++-mingw-w64-x86-64 g++-mingw-w64-i686"
    exit 1
fi

CFLAGS="-c -fno-builtin -fno-unwind-tables -fno-strict-aliasing -fno-ident -fno-stack-protector -fno-exceptions -fno-asynchronous-unwind-tables -fno-strict-overflow -fno-delete-null-pointer-checks -fpermissive -w -masm=intel -fPIC -DBEACON_SERVERLESS"

SOURCE_FILES=$(find "$BEACON_SRC" -name "*.cpp" -o -name "*.cc" | sort)

for src in $SOURCE_FILES; do
    name=$(basename "${src%.*}")

    # Skip other connector types
    case "$name" in
        ConnectorHTTP|ConnectorSMB|ConnectorTCP|ConnectorDNS|ConnectorGraph)
            continue
            ;;
    esac

    echo "    Compiling: $name (x64)"
    $COMPILER_X64 $CFLAGS -I"$BEACON_SRC" -o "$OBJECTS_DIR/x64/$name.o" "$src" 2>/dev/null || true

    echo "    Compiling: $name (x86)"
    $COMPILER_X86 $CFLAGS -I"$BEACON_SRC" -o "$OBJECTS_DIR/x86/$name.o" "$src" 2>/dev/null || true
done

# Build main variants
for variant in "" "-DBUILD_SVC" "-DBUILD_DLL" "-DBUILD_SHELLCODE"; do
    suffix=""
    case "$variant" in
        "-DBUILD_SVC") suffix="_svc" ;;
        "-DBUILD_DLL") suffix="_dll" ;;
        "-DBUILD_SHELLCODE") suffix="_sc" ;;
    esac

    MAIN_SRC="$BEACON_SRC/main.cpp"
    if [ ! -f "$MAIN_SRC" ]; then
        MAIN_SRC=$(find "$BEACON_SRC" -name "main.cpp" -o -name "Main.cpp" | head -1)
    fi

    if [ -n "$MAIN_SRC" ]; then
        echo "    Compiling: main${suffix} (x64)"
        $COMPILER_X64 $CFLAGS $variant -I"$BEACON_SRC" -o "$OBJECTS_DIR/x64/main${suffix}.o" "$MAIN_SRC" 2>/dev/null || true

        echo "    Compiling: main${suffix} (x86)"
        $COMPILER_X86 $CFLAGS $variant -I"$BEACON_SRC" -o "$OBJECTS_DIR/x86/main${suffix}.o" "$MAIN_SRC" 2>/dev/null || true
    fi
done

# Copy config template if it exists
if [ -f "$DIST_DIR/extenders/beacon_agent/objects_http/config.tpl" ]; then
    cp "$DIST_DIR/extenders/beacon_agent/objects_http/config.tpl" "$OBJECTS_DIR/"
fi

echo "[+] Built objects_serverless at $OBJECTS_DIR"
