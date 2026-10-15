#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
VERSION=${1:-}

case "$VERSION" in
	""|*[!A-Za-z0-9._-]*)
		echo "Usage: $0 VERSION (for example: v0.1.0)" >&2
		exit 1
		;;
esac

if [ -z "${AJB_EOS_CLIENT_SECRET:-}" ]; then
	echo "Set AJB_EOS_CLIENT_SECRET in the environment before packaging." >&2
	exit 1
fi

case "$(uname -s)" in
	Linux)
		case "$(uname -m)" in
			x86_64|amd64) PLATFORM=linux-amd64; BINARY=sidecar ;;
			*) echo "Unsupported Linux architecture: $(uname -m)" >&2; exit 1 ;;
		esac
		;;
	MINGW*|MSYS*|CYGWIN*)
		case "$(uname -m)" in
			x86_64|amd64) PLATFORM=windows-amd64; BINARY=sidecar.exe ;;
			*) echo "Unsupported Windows architecture: $(uname -m)" >&2; exit 1 ;;
		esac
		;;
	*)
		echo "Build releases on Linux x86_64 or Windows x86_64 (Git Bash)." >&2
		exit 1
		;;
esac

DIST_DIR="$PROJECT_DIR/dist"
PACKAGE_DIR="$DIST_DIR/package-$PLATFORM-$VERSION"
ARCHIVE="$DIST_DIR/sidecar-$PLATFORM-$VERSION.zip"

if [ -e "$PACKAGE_DIR" ] || [ -e "$ARCHIVE" ]; then
	echo "Release output already exists; choose another version or move the existing files first." >&2
	exit 1
fi

cd "$PROJECT_DIR"
./scripts/test.sh
go vet ./...
./scripts/build.sh "$DIST_DIR/$BINARY"
"$DIST_DIR/$BINARY" package \
	--config "$DIST_DIR/config.json" \
	--out "$PACKAGE_DIR" \
	--archive "$ARCHIVE"

printf 'Release asset ready: %s\n' "$ARCHIVE"
