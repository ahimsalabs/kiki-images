# kiki-images

Publishes the images in [kiki](https://github.com/ahimsalabs)'s built-in catalog again with
native EROFS layers, so that a host pulling one stores each layer as it streams instead of
converting a tar:

| Package | Source | Tags |
| --- | --- | --- |
| `ghcr.io/ahimsalabs/kiki/exeuntu` | [`ghcr.io/boldsoftware/exeuntu`](https://github.com/boldsoftware/exeuntu) `latest` | `latest`, `source-sha256-<source index>` |
| `ghcr.io/ahimsalabs/kiki/python` | [`python:3.12`](https://hub.docker.com/_/python) | `latest`, `3.12`, `source-sha256-<source index>` |

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
