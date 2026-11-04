# kiki-images

Publishes the images in [kiki](https://github.com/ahimsalabs)'s built-in catalog again with
native EROFS layers, so that a host pulling one stores each layer as it streams instead of
converting a tar:

| Package | Source | Tags |
| --- | --- | --- |
| `ghcr.io/ahimsalabs/kiki/exeuntu` | [`ghcr.io/boldsoftware/exeuntu`](https://github.com/boldsoftware/exeuntu) `latest` | `latest`, `source-sha256-<source index>` |
| `ghcr.io/ahimsalabs/kiki/python` | [`python:3.12`](https://hub.docker.com/_/python) | `latest`, `3.12`, `source-sha256-<source index>` |
| `ghcr.io/ahimsalabs/kiki/agent` | [`ghcr.io/ahimsalabs/kiki-src/agent`](images/agent), built here | `latest`, `source-sha256-<source index>` |

Each is an OCI index for linux/amd64 and linux/arm64. Each layer is containerd's native EROFS
layer (`mkfs.erofs --tar=f --aufs -Enoinline_data`, the EROFS differ's form), compressed with
zstd, as `application/vnd.erofs.layer.v1+zstd`; each config is the source's with only
`rootfs.diff_ids` replaced. Nothing else in the image changes: the files are the source's, byte
for byte.

## What it guarantees

- **Reproducible.** The output is a function of the source and the converter: erofs-utils 1.9.3
  built from its tag and checked against its commit (`tools/build-erofs-utils.sh`), a build time
  of 0 for the superblock and the directories mkfs.erofs makes up (`-T0 --mkfs-time`), a UUID
  from each layer's diff_id, `SOURCE_DATE_EPOCH` unset, run as uid 0 (a user namespace), and zstd
  from klauspost/compress with one encoder. Publishing one source twice yields the same index
  digest (`TestPublish`).
- **Traceable.** The index and each manifest carry `org.opencontainers.image.base.name` and
  `org.opencontainers.image.base.digest` (the source index, and each platform's source
  manifest), `org.opencontainers.image.source` (this repository) and
  `net.ahimsalabs.kiki.converter` (the format revision, mkfs.erofs's version and flags, the
  zstd module's version). Each layer descriptor names the source layer's digest and diff_id.
  The tag `source-sha256-<hex>` names the source index.
- **Checked.** Every blob passes `fsck.erofs --extract` (all of its data read) before it is
  compressed. Before anything is pushed to ghcr.io, the workflow converts into a registry on the
  runner and `kiki-imagepub -verify` loop-mounts every layer of every platform with the kernel's
  erofs and compares it with the source tar layer, entry for entry: kind, mode, owner, mtime,
  content, symlink target, device numbers, xattrs, whiteouts, opaque directories and hardlinks.
  It refuses what the native form cannot carry faithfully: a directory mkfs.erofs makes up (the
  tar names a path below it but not it) over a lower layer's directory, whose mode, owner and
  time it would replace in an overlay (the layer's root aside, which kiki takes as implicit),
  and a whiteout of a path the same layer holds. Only then is the index copied to ghcr.io,
  unchanged. A layer the pinned mkfs.erofs cannot convert (1.9.3 refuses a `.wh..wh..opq` at the
  layer's root, for one) fails the run, and nothing is pushed.
- **Tags move only to a verified index.** kiki's catalog names these repositories by tag
  (`latest`), not digest, and each host pulls what the tag points at when it creates a VM. So a
  moving tag (`latest`, `3.12`) moves only to an index the same run has verified as above: a
  new source's staged index, or, for a source ghcr.io already holds, that index as ghcr.io
  serves it. A run that finds every tag already on its source's index moves nothing.
- **Checked again by its consumer.** Every kiki host validates each native layer as it pulls it,
  and kiki's daily canary workflow pulls whatever `latest` is through kiki's own resolver and
  checks that its merged rootfs equals the source's, entry for entry, opening an issue in kiki
  when it does not.

## The agent image

`images/agent` builds an image for coding agents: Ubuntu 26.04 with systemd, run as `kikiu`
with passwordless sudo, and the tools agents reach for: Go, Node.js (npm, pnpm and yarn through
corepack), Bun, jj, Task, gh, ripgrep, uv, Python, Docker Engine (from Docker's apt repository,
socket-activated), agent-browser and a headless Chromium that Playwright finds without
configuration, gcc, g++ and make, jq and common debugging tools. Rust's toolchain ships as its
compressed dist files: the first `cargo` or `rustc` installs it for the user from them, offline.
Its other packages all come from Ubuntu's main component, which Ubuntu's security team maintains.

Every input is pinned in `images/agent/versions.env`: the base image by digest, Ubuntu's packages
by a [snapshot.ubuntu.com](https://snapshot.ubuntu.com) timestamp, Docker's by apt version, and
each tool by version and sha256 for each architecture, checked as it is downloaded. Each tool is
fetched in a build stage of its own and copied in with `COPY --link`, and the build rewrites
every timestamp to `SOURCE_DATE_EPOCH`. So a layer built from the same inputs has the same
digest on any runner, and a bump changes only its own layers: a new apt snapshot the apt and
Docker layers, a Go release the Go layer. A host pulls only those.

`.github/workflows/build-agent.yml` builds it natively on amd64 and arm64, runs
`images/agent/smoke.sh` in it (every tool, an offline Rust build, a page through agent-browser
and Playwright), boots its systemd, and reports a Trivy scan and how many layers the published
image already has. On main it pushes the result to `ghcr.io/ahimsalabs/kiki-src/agent`, which
`publish.yml` then converts and verifies like any other source. Locally, with a docker-container
builder (`docker buildx create --use`):
`images/agent/build.sh --output type=docker,name=agent,rewrite-timestamp=true`.

### Keeping it current

`.github/workflows/bump-agent.yml` updates `versions.env` through one pull request (branch
`bot/agent-bump`), using `cmd/agent-bump`:

- **Weekly**, every tool moves to its newest release at least three days old, and Docker to
  its repository's newest packages. Its sha256 comes from
  where its publisher records it: Go's download list, Node.js's `SHASUMS256.txt`, GitHub's asset
  digests, rustup's `.sha256` files, and the Rust channel manifest. Chrome for Testing publishes
  none, so the bot records the sha256 of what it downloads and the pull request shows it for
  review. The base image moves to the tag's current digest and apt to that day's snapshot.
- **Nightly**, Trivy scans `kiki-src/agent:latest`. A HIGH or CRITICAL vulnerability with a fix
  bumps only what carries it, without the cooldown: the apt snapshot for Ubuntu's packages,
  Docker's packages for Docker, the tool for a tool's binary. The scans skip `linux-libc-dev`,
  the kernel's headers, which trackers mark with every kernel CVE although none of it runs: kiki
  boots its own kernel (`images/agent/trivy-ignore.rego`). A vulnerability whose fix needs a tool release that doesn't exist
  yet is tracked in one issue.

`Build agent image` gates the pull request. If every pin moves by at most a patch release, the
pull request merges itself once that check passes; anything larger waits for a review. A merge
then builds, converts, verifies and publishes as above. Dependabot keeps the workflows' actions
and this module's Go dependencies current.

Once, an org admin installs a GitHub App on this repository with contents, pull request and
issue write. Its ID goes in `vars.AGENT_BOT_APP_ID` and its key in `secrets.AGENT_BOT_PRIVATE_KEY`,
because pull requests opened with `GITHUB_TOKEN` start no workflows. The admin also allows
auto-merge and makes both `Build agent image` jobs required checks of `main`.

## Publishing

`.github/workflows/publish.yml` runs daily and on demand (`gh workflow run publish.yml`, with
`-f force=true` to convert a source already published). A source ghcr.io already holds,
converted by the same converter id, is verified and re-tagged, not converted; when exeuntu's
`latest` or `python:3.12` moves upstream, the new source is converted, verified, and the tags
move with it.

Hosts pull anonymously, so the packages must be public. ghcr.io links each package to this
repository (`org.opencontainers.image.source`); a new package may still start private, and an
org admin makes it public once, at
`https://github.com/orgs/ahimsalabs/packages/container/package/kiki%2F<name>/settings`. The
workflow fails its last step until an anonymous pull succeeds.

Locally (Linux, Go, erofs-utils 1.9.3 from `tools/build-erofs-utils.sh` on `$PATH`):

```sh
go run ./cmd/kiki-imagepub -source ghcr.io/boldsoftware/exeuntu:latest \
    -dest localhost:5000/kiki/exeuntu -tag latest
sudo kiki-imagepub -verify localhost:5000/kiki/exeuntu@sha256:…
```

## Licensing

The code in this repository is licensed under the Apache License 2.0 (`LICENSE`).

The published images are redistributions of their sources, unmodified but for the layer
encoding, and the licenses of the software in them apply as they do to the source images:

- **exeuntu** is built by Bold Software from
  [boldsoftware/exeuntu](https://github.com/boldsoftware/exeuntu), whose build files are
  Apache-2.0. The image holds Ubuntu packages, each under its own license (see
  `/usr/share/doc/*/copyright` in the image), and the third-party tools exeuntu installs, under
  theirs.
- **agent** is built here from `images/agent` (Apache-2.0, as this repository). Its systemd
  configuration is adapted from exeuntu's (see
  `/usr/local/share/kiki-agent/NOTICE` in the image). The image holds Ubuntu packages, each
  under its own license (see `/usr/share/doc/*/copyright` in the image), and the release
  binaries of the tools it installs (Go, Node.js, Bun, jj, Task, gh, ripgrep, uv, agent-browser,
  Chrome for Testing, rustup and Rust) and Docker Engine's packages, each under its own.
- **python** is the Docker Official Image [`python`](https://hub.docker.com/_/python), built from
  [docker-library/python](https://github.com/docker-library/python) (MIT). The image holds
  Python (PSF License Agreement) and Debian packages, each under its own license (see
  `/usr/share/doc/*/copyright` in the image).

As with any image, it is the user's responsibility to ensure that their use complies with the
licenses of the software it contains. Source code for the GPL and LGPL packages in the images is
available from the distributions' archives (Ubuntu, Debian) for the package versions the images
name.

erofs-utils (GPL-2.0+) is built and run by the workflow to write the layers; it is not part of
the published images.
