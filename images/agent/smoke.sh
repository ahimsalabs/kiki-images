#!/bin/bash
# Checks, as the login user, that every tool in the image runs. Exits
# non-zero at the first failure. CI runs it in each built image:
#   docker run --rm --entrypoint sudo IMAGE -u exedev -i /usr/local/share/kiki-agent/smoke.sh
# The Rust check runs without a network when unshare can make one (CI's
# privileged runs), to prove the first-use install is offline.
set -euo pipefail
cd "$(mktemp -d)"
step() { printf '\n== %s\n' "$*"; }

step whoami
test "$(id -un)" = exedev
sudo -n true

step apt
! grep -q '^Snapshot:' /etc/apt/sources.list.d/ubuntu.sources
apt-cache policy coreutils-from-uutils | grep -q 'Candidate: (none)'

step versions
ls --version | head -1 | grep -q 'GNU coreutils' || { echo 'coreutils is not GNU' >&2; exit 1; }
git --version
curl --version | head -1
python3 --version
uv --version
go version
node --version
npm --version
pnpm --version </dev/null || echo "(pnpm: corepack fetches it on first use)"
bun --version
jj --version
task --version
gh --version | head -1
rg --version | head -1
jq --version
sqlite3 --version
agent-browser --version
chrome-headless-shell --version
docker --version
docker compose version
docker buildx version

step debugging tools
for t in strace lsof tcpdump ip ping nc dig htop tmux rsync tree zip patch wget vi killall pstree; do
	command -v "$t" >/dev/null || { echo "missing: $t" >&2; exit 1; }
done
echo all present

step go
cat >main.go <<'GO'
package main

func main() { println("hello from go") }
GO
go mod init smoke >/dev/null 2>&1
go build -o hello . && ./hello

step jj
jj git init repo >/dev/null && jj -R repo status

step bun
mkdir bunproj && (cd bunproj && echo '{"name":"b","private":true}' >package.json && bun add -d typescript >/dev/null && bunx tsc --version)

step npm
mkdir npmproj && (cd npmproj && npm init -y >/dev/null && npm i --silent esbuild && npx esbuild --version)
npm i -g --silent cowsay && command -v cowsay

step rust
offline=()
if sudo -n unshare -n true 2>/dev/null; then offline=(sudo -n unshare -n sudo -u "$(id -un)" -E HOME="$HOME" PATH="$PATH"); fi
"${offline[@]}" cargo new --quiet hello-rs
(cd hello-rs && "${offline[@]}" cargo run --quiet --offline)
cargo clippy --version
rustfmt --version

step browser
cat >page.html <<'HTML'
<!doctype html><title>smoke</title><h1>Hello</h1><button>Go</button><a href="#x">link</a>
HTML
agent-browser open "file://$PWD/page.html" >/dev/null
agent-browser snapshot | tee snap.txt
grep -q 'heading "Hello"' snap.txt
agent-browser close >/dev/null
mkdir pw && (cd pw && npm init -y >/dev/null && npm i --silent "playwright-core@${PLAYWRIGHT_VERSION:-1.64.1}" && node -e '
const { chromium } = require("playwright-core");
(async () => {
  const b = await chromium.launch();
  const p = await b.newPage();
  await p.goto("file://" + process.argv[1]);
  console.log("playwright:", b.version(), await p.title());
  await b.close();
})().catch(e => { console.error(e); process.exit(1); });' "$PWD/../page.html")

echo
echo "smoke: ok"
