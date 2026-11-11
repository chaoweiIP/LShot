#!/bin/zsh
set -eu
cd -- "${0:A:h}"
app="LShot.app"
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/lshot-build.XXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT

for arch in arm64 x86_64; do
    xcrun swiftc -module-cache-path "${TMPDIR:-/tmp}/longshot-swift-cache" \
        -O -target "$arch-apple-macosx15.2" Sources/*.swift -o "$build_dir/LShot-$arch"
done
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
xcrun lipo -create "$build_dir/LShot-arm64" "$build_dir/LShot-x86_64" -output "$app/Contents/MacOS/LShot"
cp Info.plist "$app/Contents/Info.plist"
cp Resources/Lshot.icns "$app/Contents/Resources/"
codesign --force --sign - "$app"
