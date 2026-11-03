#!/bin/bash
# fetch.sh URL SHA256 DEST [STRIP]
#
# Downloads URL, checks it against SHA256 and fails if it does not match,
# then unpacks it into DEST: a .tar.gz, .tgz or .tar.xz is extracted (with
# --strip-components=STRIP when given), a .zip is unzipped, and anything
# else is installed as the executable file DEST.
set -euo pipefail
# A build behind a TLS-intercepting proxy mounts its CA bundle here.
if [[ -s /run/secrets/ca ]]; then export CURL_CA_BUNDLE=/run/secrets/ca; fi
url=$1 sha=$2 dest=$3 strip=${4:-}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
file="$tmp/${url##*/}"
curl -fsSL --retry 3 -o "$file" "$url"
echo "$sha  $file" | sha256sum -c --quiet -
case "$url" in
*.tar.gz | *.tgz | *.tar.xz)
	mkdir -p "$dest"
	tar -xf "$file" -C "$dest" --no-same-owner ${strip:+--strip-components="$strip"}
	;;
*.zip)
	mkdir -p "$dest"
	unzip -q "$file" -d "$dest"
	;;
*)
	install -D -m 755 "$file" "$dest"
	;;
esac
