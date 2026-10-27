#!/bin/sh
# Builds the release archives into dist/. The release workflow runs this on a
# macOS runner; it also runs locally.
#
#   scripts/release.sh v0.1.0
#
# macOS gets one universal binary (Apple Silicon and Intel), unsigned here;
# scripts/sign-macos.sh signs it on the maintainer's Mac before the release is
# published. Archive names carry no version, so
# .../releases/latest/download/<name> always points at the newest one.
set -eu
cd "$(dirname "$0")/.."
version=${1:?usage: scripts/release.sh vX.Y.Z}
ldflags="-s -w -X github.com/Ryheff24/agentlog/internal/app.Version=$version"
rm -rf dist && mkdir -p dist/build

build() { # GOOS GOARCH OUT
	CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$ldflags" -o "$3" ./cmd/agentlog
}

package() { # DIR NAME: dist/NAME.tar.gz holding DIR/agentlog
	tar -C "$1" -czf "dist/$2.tar.gz" agentlog
}

mkdir -p dist/build/darwin
build darwin arm64 dist/build/agentlog-arm64
build darwin amd64 dist/build/agentlog-amd64
lipo -create -output dist/build/darwin/agentlog dist/build/agentlog-arm64 dist/build/agentlog-amd64
package dist/build/darwin agentlog-darwin-universal

for arch in amd64 arm64; do
	mkdir -p "dist/build/linux-$arch"
	build linux "$arch" "dist/build/linux-$arch/agentlog"
	package "dist/build/linux-$arch" "agentlog-linux-$arch"
done

rm -rf dist/build
(cd dist && shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt)
ls -l dist
