#!/bin/bash
# rust-dist.sh VERSION MANIFEST_SHA256 TARGET DEST
#
# Mirrors the Rust toolchain VERSION for TARGET into DEST, laid out like
# static.rust-lang.org, so that rustup installs it offline with
# RUSTUP_DIST_SERVER=file://DEST: the channel manifest (checked against
# MANIFEST_SHA256), served as the stable channel, and the packages of the
# minimal profile plus clippy and rustfmt, each checked against the hash the
# manifest gives it.
set -euo pipefail
version=$1 sha=$2 target=$3 dest=$4
if [[ -s /run/secrets/ca ]]; then export CURL_CA_BUNDLE=/run/secrets/ca; fi
mkdir -p "$dest/dist"
manifest=$dest/dist/channel-rust-stable.toml
curl -fsSL --retry 3 -o "$manifest" "https://static.rust-lang.org/dist/channel-rust-$version.toml"
echo "$sha  $manifest" | sha256sum -c --quiet -
(cd "$dest/dist" && sha256sum channel-rust-stable.toml >channel-rust-stable.toml.sha256)
python3 - "$manifest" "$target" <<'PY' | while read -r url hash; do
import sys, tomllib
manifest = tomllib.load(open(sys.argv[1], "rb"))
for pkg in ("rustc", "cargo", "rust-std", "clippy-preview", "rustfmt-preview"):
    t = manifest["pkg"][pkg]["target"][sys.argv[2]]
    print(t["xz_url"], t["xz_hash"])
PY
	file=$dest/dist/${url#https://static.rust-lang.org/dist/}
	mkdir -p "$(dirname "$file")"
	curl -fsSL --retry 3 -o "$file" "$url"
	echo "$hash  $file" | sha256sum -c --quiet -
done
