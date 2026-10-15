#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
SDK_ROOT=${EOS_SDK_ROOT:-$(CDPATH= cd -- "$PROJECT_DIR/../SDK" && pwd)}
OUTPUT=${1:-"$PROJECT_DIR/dist/sidecar"}

if [ ! -f "$SDK_ROOT/Include/eos_sdk.h" ]; then
	echo "EOS SDK headers not found under $SDK_ROOT; set EOS_SDK_ROOT." >&2
	exit 1
fi

case "$(uname -s)" in
	Linux)
		case "$(uname -m)" in
			x86_64|amd64) LIBRARY=libEOSSDK-Linux-Shipping.so ;;
			aarch64|arm64) LIBRARY=libEOSSDK-LinuxArm64-Shipping.so ;;
			*) echo "Unsupported Linux architecture: $(uname -m)" >&2; exit 1 ;;
		esac
		LINK_FLAGS="-L$SDK_ROOT/Bin -l:$LIBRARY -Wl,-rpath,\$ORIGIN -lstdc++ -pthread"
		;;
	Darwin)
		LIBRARY=libEOSSDK-Mac-Shipping.dylib
		LINK_FLAGS="-L$SDK_ROOT/Bin -lEOSSDK-Mac-Shipping -Wl,-rpath,@loader_path -lc++"
		;;
	MINGW*|MSYS*|CYGWIN*)
		LIBRARY=EOSSDK-Win64-Shipping.dll
		LINK_FLAGS="-L$SDK_ROOT/Lib -lEOSSDK-Win64-Shipping -lstdc++"
		;;
	*)
		echo "Unsupported build environment: $(uname -s)" >&2
		exit 1
		;;
esac

BUNDLE_KEY_HEX=$(openssl rand -hex 32) || {
	echo "Could not generate the build-specific config encryption key." >&2
	exit 1
}

LIBRARY_PATH="$SDK_ROOT/Bin/$LIBRARY"

if [ ! -f "$LIBRARY_PATH" ]; then
	echo "EOS SDK runtime not found: $LIBRARY_PATH" >&2
	exit 1
fi

mkdir -p "$(dirname -- "$OUTPUT")"
case "$(uname -s)" in
	MINGW*|MSYS*|CYGWIN*)
		case "$OUTPUT" in
			*.exe) ;;
			*) OUTPUT="$OUTPUT.exe" ;;
		esac
		;;
esac
(
	cd "$PROJECT_DIR"
	CGO_ENABLED=1 \
	CGO_CFLAGS="-I$SDK_ROOT/Include" \
	CGO_CXXFLAGS="-I$SDK_ROOT/Include -std=c++17" \
	CGO_LDFLAGS="$LINK_FLAGS" \
	go build -ldflags "-X main.bundleKeyHex=$BUNDLE_KEY_HEX" -o "$OUTPUT" ./cmd/sidecar
)
cp "$LIBRARY_PATH" "$(dirname -- "$OUTPUT")/"
echo "Built $OUTPUT against EOS SDK at $SDK_ROOT"
