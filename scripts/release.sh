#!/bin/sh
# Build release archives for every platform and publish them as a GitHub
# release. There is no CI: this runs on the author's machine, after the gate.
#
#   scripts/release.sh 1.0.0 [--publish]
#
# Asset names follow the <name>_<version>_<os>_<arch>.tar.gz convention that
# mise's github backend and aqua detect without extra configuration.
set -eu
version="${1:?usage: scripts/release.sh X.Y.Z [--publish]}"
case "$version" in v*) echo "pass the version without a leading v" >&2; exit 2 ;; esac
publish="${2:-}"
cd "$(dirname "$0")/.."

test -z "$(git status --porcelain)" || { echo "working tree is dirty" >&2; exit 1; }
grep -q "^version = \"$version\"" keel.toml || { echo "keel.toml does not pin $version" >&2; exit 1; }
# Gate with the code being released: the release it pins does not exist yet.
go run ./cmd/keel gate

out="dist/$version"
rm -rf "$out" && mkdir -p "$out"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  dir="$out/keel_${version}_${os}_${arch}"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X github.com/visual-z/keel/internal/keel.Version=$version" \
    -o "$dir/keel" ./cmd/keel
  cp README.md "$dir/"
  tar -C "$out" -czf "$dir.tar.gz" "$(basename "$dir")"
  rm -rf "$dir"
done
(cd "$out" && shasum -a 256 *.tar.gz > checksums.txt)
cat "$out/checksums.txt"

if [ "$publish" = "--publish" ]; then
  git tag -a "v$version" -m "keel $version"
  git push origin "v$version"
  gh release create "v$version" "$out"/*.tar.gz "$out/checksums.txt" \
    --title "keel $version" --notes "See README.md. Verify downloads against checksums.txt."
fi
