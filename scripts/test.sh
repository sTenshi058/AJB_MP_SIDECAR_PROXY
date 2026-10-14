#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
SDK_ROOT=${EOS_SDK_ROOT:-$(CDPATH= cd -- "$PROJECT_DIR/../SDK" && pwd)}

case "$(uname -s)" in
	Linux)
		case "$(uname -m)" in
			x86_64|amd64) LIBRARY=libEOSSDK-Linux-Shipping.so ;;
			aarch64|arm64) LIBRARY=libEOSSDK-LinuxArm64-Shipping.so ;;
			*) echo "Unsupported Linux architecture: $(uname -m)" >&2; exit 1 ;;
		esac
		LINK_FLAGS="-L$SDK_ROOT/Bin -l:$LIBRARY -Wl,-rpath,$SDK_ROOT/Bin -lstdc++ -pthread"
		LD_LIBRARY_PATH="$SDK_ROOT/Bin${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
		;;
	Darwin)
		LINK_FLAGS="-L$SDK_ROOT/Bin -lEOSSDK-Mac-Shipping -Wl,-rpath,$SDK_ROOT/Bin -lc++"
		DYLD_LIBRARY_PATH="$SDK_ROOT/Bin${DYLD_LIBRARY_PATH:+:$DYLD_LIBRARY_PATH}"
		;;
	MINGW*|MSYS*|CYGWIN*)
		if [ ! -f "$SDK_ROOT/Bin/EOSSDK-Win64-Shipping.dll" ]; then
			echo "EOS SDK runtime not found: $SDK_ROOT/Bin/EOSSDK-Win64-Shipping.dll" >&2
			exit 1
		fi
		LINK_FLAGS="-L$SDK_ROOT/Lib -lEOSSDK-Win64-Shipping -lstdc++"
		PATH="$SDK_ROOT/Bin:$PATH"
		;;
	*)
		echo "This script runs tests on Linux and macOS." >&2
		exit 1
		;;
esac

(
	cd "$PROJECT_DIR"
	CGO_ENABLED=1 \
	CGO_CFLAGS="-I$SDK_ROOT/Include" \
	CGO_CXXFLAGS="-I$SDK_ROOT/Include -std=c++17" \
	CGO_LDFLAGS="$LINK_FLAGS" \
	PATH="${PATH:-}" \
	LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-}" \
	DYLD_LIBRARY_PATH="${DYLD_LIBRARY_PATH:-}" \
	go test -count=1 -race ./...
)
