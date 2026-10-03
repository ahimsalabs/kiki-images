#!/usr/bin/env bash
# build-erofs-utils.sh: build the mkfs.erofs that writes the published
# native EROFS layers, and the fsck.erofs that checks them
# (internal/imagepub, .github/workflows/publish.yml): erofs-utils at the
# pinned tag, checked against its commit, with no compressors (the layers
# are uncompressed erofs), into <prefix>/bin.
#
# Usage: tools/build-erofs-utils.sh <prefix>
#
# Needs git, a C toolchain, autoconf, automake, libtool and pkg-config
# (Debian/Ubuntu: build-essential autoconf automake libtool pkg-config
# uuid-dev).
set -euo pipefail

VERSION=1.9.3
# The commit tag v1.9.3 names (git ls-remote ... refs/tags/v1.9.3^{}).
COMMIT=7db78788b000999e2de88decd2ba90654f26171c
REPO=https://git.kernel.org/pub/scm/linux/kernel/git/xiang/erofs-utils.git

prefix=${1:?usage: $0 <prefix>}
mkdir -p "$prefix"
prefix=$(cd "$prefix" && pwd)
src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT

git -c advice.detachedHead=false clone --quiet --depth 1 --branch "v$VERSION" "$REPO" "$src"
got=$(git -C "$src" rev-parse HEAD)
if [[ $got != "$COMMIT" ]]; then
	echo "build-erofs-utils: v$VERSION is $got, want $COMMIT" >&2
	exit 1
fi
cd "$src"
./autogen.sh >/dev/null
./configure --quiet --prefix="$prefix" --disable-lz4 --disable-lzma --without-zlib \
	--without-libdeflate --without-libzstd --without-qpl --without-xxhash --disable-fuse \
	--without-selinux
make --quiet -j"$(nproc)" >/dev/null
make --quiet install >/dev/null
"$prefix/bin/mkfs.erofs" --version | head -1
