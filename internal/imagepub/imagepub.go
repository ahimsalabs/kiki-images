// Package imagepub publishes an OCI image again with native EROFS layers:
// each tar layer of each platform converted by a pinned mkfs.erofs into
// containerd's native layer form (the EROFS differ's
// "--tar=f --aufs -Enoinline_data"), compressed with zstd and pushed as
// application/vnd.erofs.layer.v1+zstd, so that a consumer that reads
// native layers (kiki; containerd's EROFS snapshotter) stores each layer
// as it streams instead of converting the tar. cmd/kiki-imagepub runs it,
// and .github/workflows/publish.yml publishes kiki's catalog images with
// it to ghcr.io/ahimsalabs/kiki/<name>.
//
// The output is a function of the source image and the converter alone,
// so publishing one source twice yields the same index digest:
//
//   - mkfs.erofs is the pinned release (MkfsVersion), run with a build
//     time of 0 that it applies to nothing but the superblock and the
//     directories it makes up (-T0 --mkfs-time), a UUID taken from the
//     layer's diff_id, SOURCE_DATE_EPOCH unset, and as uid and gid 0 (a
//     user namespace when not root), which it gives the root it makes up
//     for a tar without "./";
//   - zstd is klauspost/compress at the version this binary links, one
//     encoder, no concurrency;
//   - the configs are the source's with only rootfs.diff_ids replaced,
//     and the annotations name the source, never the time or the run.
//
// Every blob passes fsck.erofs, which reads all of its data, before it is
// compressed; Verify (Linux, as root) then mounts each published layer
// and compares it with its source tar layer, entry for entry. Whether
// kiki accepts the layers is checked in kiki, which validates a published
// image before its catalog names it.
package imagepub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/klauspost/compress/zstd"
)

// MkfsVersion is the erofs-utils release that writes the layers, as
// mkfs.erofs --version prints it. The workflow builds it from its tag;
// another version may write other bytes for the same tar, so Publish
// refuses one.
const MkfsVersion = "1.9.3"

// LayerMediaType is the published layers' media type: containerd's
// native EROFS layer, zstd-compressed.
const LayerMediaType types.MediaType = "application/vnd.erofs.layer.v1+zstd"

// formatRevision is bumped whenever the published form changes other
// than through MkfsVersion or the zstd module's version, which the
// converter id names on their own.
const formatRevision = 1

// mkfsArgs are containerd's EROFS differ's flags (--tar=f --aufs
// -Enoinline_data, 4 KiB blocks), then those that make the blob a
// function of the tar.
var mkfsArgs = []string{"--tar=f", "--aufs", "--quiet", "-b4096", "-Enoinline_data", "-T0", "--mkfs-time"}

// Annotations. The OCI base.* ones name the source; the kiki ones the
// converter, and each layer's source layer.
const (
	AnnotationBaseName    = "org.opencontainers.image.base.name"
	AnnotationBaseDigest  = "org.opencontainers.image.base.digest"
	AnnotationSource      = "org.opencontainers.image.source"
	AnnotationDescription = "org.opencontainers.image.description"
	AnnotationConverter   = "net.ahimsalabs.kiki.converter"
	// On each layer descriptor: the source layer's digest and diff_id.
	AnnotationLayerSourceDigest = "net.ahimsalabs.kiki.source.digest"
	AnnotationLayerSourceDiffID = "net.ahimsalabs.kiki.source.diff-id"
)

// sourceRepo is the repository that holds the publisher, which ghcr.io
// links the packages to, and from which they take their visibility.
const sourceRepo = "https://github.com/ahimsalabs/kiki-images"

// ConverterID names the converter in the published annotations: this
// package's format, mkfs.erofs and its flags, and the zstd module.
func ConverterID() string {
	return fmt.Sprintf("kiki-imagepub/%d mkfs.erofs/%s (%s -U<diff_id>) zstd/github.com/klauspost/compress@%s (better, 1 encoder)",
		formatRevision, MkfsVersion, strings.Join(mkfsArgs, " "), zstdVersion())
}

func zstdVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/klauspost/compress" {
				if d.Replace != nil {
					d = d.Replace
				}
				return d.Version
			}
		}
	}
	return "unknown"
}

// SourceTag is the tag Publish gives the index converted from the source
// index of digest src: "source-sha256-<hex>".
func SourceTag(src v1.Hash) string { return "source-" + src.Algorithm + "-" + src.Hex }

// Options are what to publish and where.
type Options struct {
	// Source is the image to convert, a tag or a digest of an index.
	Source string
	// Dest is the repository to push to, e.g. ghcr.io/ahimsalabs/kiki/exeuntu.
	Dest string
	// Platforms to convert; each must be in the source index. Default
	// linux/amd64 and linux/arm64.
	Platforms []v1.Platform
	// Tags, beside SourceTag, to point at the index, e.g. latest.
	Tags []string
	// Mkfs is the mkfs.erofs to run; default the one on $PATH.
	Mkfs string
	// Fsck is the fsck.erofs that checks each blob; default the one
	// beside Mkfs, else the one on $PATH.
	Fsck string
	// WorkDir holds the converted layers until they are pushed: about the
	// compressed image, and the largest uncompressed layer, per platform.
	// Default os.TempDir().
	WorkDir string
	// Force converts and pushes even if Dest already has the index
	// converted from this source by this converter.
	Force bool
	// DryRun converts and validates every layer, and pushes nothing.
	DryRun bool
	// CheckOnly reports whether Dest has the index converted from this
	// source by this converter, converting and pushing nothing; Ref is
	// empty when it does not, and Current says whether every one of Tags
	// already points at it.
	CheckOnly bool
	// Remote are go-containerregistry's options for both registries
	// (auth, transport); Publish adds the context.
	Remote []remote.Option
	Logger *slog.Logger
}

// Result is what Publish published, or found published.
type Result struct {
	Source    string            `json:"source"`    // the source index, repository@digest
	Ref       string            `json:"ref"`       // the published index, repository@digest
	Tags      []string          `json:"tags"`      // every tag pointing at it
	Platforms map[string]string `json:"platforms"` // platform -> manifest digest
	Converter string            `json:"converter"`
	// Found is true when Dest already had it.
	Found bool `json:"found"`
	// Pushed is false when Dest already had it (or DryRun, CheckOnly).
	Pushed bool `json:"pushed"`
	// Current is true, with CheckOnly, when Dest had it and every one of
	// Options.Tags already points at it: publishing would move no tag.
	Current bool `json:"current"`
}

// Publish converts opts.Source and pushes it to opts.Dest, or finds it
// there already.
func Publish(ctx context.Context, opts Options) (*Result, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if len(opts.Platforms) == 0 {
		opts.Platforms = []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}}
	}
	if opts.WorkDir == "" {
		opts.WorkDir = os.TempDir()
	}
	src, err := name.ParseReference(opts.Source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	dest, err := name.NewRepository(opts.Dest)
	if err != nil {
		return nil, fmt.Errorf("dest: %w", err)
	}
	ropts := append(append([]remote.Option{}, opts.Remote...), remote.WithContext(ctx))

	desc, err := remote.Get(src, ropts...)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", src, err)
	}
	if !desc.MediaType.IsIndex() {
		return nil, fmt.Errorf("source %s is a %s, not an index: publish takes a multi-platform image", src, desc.MediaType)
	}
	srcIdx, err := desc.ImageIndex()
	if err != nil {
		return nil, err
	}
	srcIM, err := srcIdx.IndexManifest()
	if err != nil {
		return nil, err
	}
	srcDigest := src.Context().Digest(desc.Digest.String())
	conv := ConverterID()
	tag := SourceTag(desc.Digest)
	res := &Result{Source: srcDigest.String(), Converter: conv, Tags: append([]string{tag}, opts.Tags...)}

	if opts.CheckOnly || (!opts.Force && !opts.DryRun) {
		if pub, ok := published(dest.Tag(tag), desc.Digest, conv, opts.Platforms, ropts); ok {
			res.Ref = dest.Digest(pub.digest.String()).String()
			res.Platforms = pub.platforms
			res.Found = true
			opts.Logger.Info("already published", "source", res.Source, "ref", res.Ref)
			if opts.CheckOnly {
				res.Current = tagged(dest, opts.Tags, pub.digest, ropts)
				return res, nil
			}
			if err := tagAll(dest, opts.Tags, pub.idx, ropts); err != nil {
				return nil, err
			}
			return res, nil
		}
	}
	if opts.CheckOnly {
		return res, nil
	}
	if opts.Mkfs == "" {
		p, err := exec.LookPath("mkfs.erofs")
		if err != nil {
			return nil, fmt.Errorf("mkfs.erofs %s (erofs-utils) not found: %w", MkfsVersion, err)
		}
		opts.Mkfs = p
	}
	if err := checkTool(ctx, opts.Mkfs, "mkfs.erofs"); err != nil {
		return nil, err
	}
	if opts.Fsck == "" {
		opts.Fsck = filepath.Join(filepath.Dir(opts.Mkfs), "fsck.erofs")
		if _, err := os.Stat(opts.Fsck); err != nil {
			p, err := exec.LookPath("fsck.erofs")
			if err != nil {
				return nil, fmt.Errorf("fsck.erofs %s (erofs-utils) not found: %w", MkfsVersion, err)
			}
			opts.Fsck = p
		}
	}
	if err := checkTool(ctx, opts.Fsck, "fsck.erofs"); err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp(opts.WorkDir, "kiki-imagepub-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	res.Platforms = map[string]string{}
	for _, p := range opts.Platforms {
		d, err := platformManifest(srcIM, p)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", srcDigest, err)
		}
		srcImg, err := srcIdx.Image(d.Digest)
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(work, strings.ReplaceAll(p.String(), "/", "-"))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, err
		}
		cv := &converter{mkfs: opts.Mkfs, fsck: opts.Fsck, dir: dir, log: opts.Logger.With("platform", p.String())}
		img, err := cv.image(ctx, srcImg, map[string]string{
			AnnotationBaseName:   src.Context().Name(),
			AnnotationBaseDigest: d.Digest.String(),
			AnnotationSource:     sourceRepo,
			AnnotationConverter:  conv,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		mh, err := img.Digest()
		if err != nil {
			return nil, err
		}
		res.Platforms[p.String()] = mh.String()
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Platform: d.Platform})
	}
	idx = mutate.Annotations(idx, map[string]string{
		AnnotationBaseName:    src.Context().Name(),
		AnnotationBaseDigest:  desc.Digest.String(),
		AnnotationSource:      sourceRepo,
		AnnotationConverter:   conv,
		AnnotationDescription: fmt.Sprintf("%s@%s with native EROFS layers for kiki; a redistribution of that image, whose license terms apply", src.Context().Name(), desc.Digest),
	}).(v1.ImageIndex)
	h, err := idx.Digest()
	if err != nil {
		return nil, err
	}
	res.Ref = dest.Digest(h.String()).String()
	if opts.DryRun {
		opts.Logger.Info("dry run: not pushed", "ref", res.Ref)
		return res, nil
	}
	start := time.Now()
	if err := remote.WriteIndex(dest.Tag(tag), idx, ropts...); err != nil {
		return nil, fmt.Errorf("push %s: %w", dest.Tag(tag), err)
	}
	if err := tagAll(dest, opts.Tags, idx, ropts); err != nil {
		return nil, err
	}
	opts.Logger.Info("pushed", "ref", res.Ref, "tags", res.Tags, "took", time.Since(start).Round(time.Millisecond))
	res.Pushed = true
	return res, nil
}

func tagAll(dest name.Repository, tags []string, idx v1.ImageIndex, ropts []remote.Option) error {
	for _, t := range tags {
		if err := remote.Tag(dest.Tag(t), idx, ropts...); err != nil {
			return fmt.Errorf("tag %s: %w", dest.Tag(t), err)
		}
	}
	return nil
}

// tagged reports whether every one of tags in dest points at digest. Any
// failure to read one means it does not.
func tagged(dest name.Repository, tags []string, digest v1.Hash, ropts []remote.Option) bool {
	for _, t := range tags {
		d, err := remote.Head(dest.Tag(t), ropts...)
		if err != nil || d.Digest != digest {
			return false
		}
	}
	return true
}

// Copy pushes the index src, which Publish made (in a staging registry,
// say, where it was verified), to the repository dest as it is, tagged
// with its SourceTag and tags. The digest stays src's: Copy only moves
// bytes, so what was verified is what dest serves.
func Copy(ctx context.Context, src, dest string, tags []string, ropts []remote.Option, log *slog.Logger) (*Result, error) {
	if log == nil {
		log = slog.Default()
	}
	from, err := name.NewDigest(src)
	if err != nil {
		return nil, fmt.Errorf("copy source must be a digest reference: %w", err)
	}
	to, err := name.NewRepository(dest)
	if err != nil {
		return nil, fmt.Errorf("dest: %w", err)
	}
	ropts = append(append([]remote.Option{}, ropts...), remote.WithContext(ctx))
	idx, err := remote.Index(from, ropts...)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", from, err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	base, conv := im.Annotations[AnnotationBaseDigest], im.Annotations[AnnotationConverter]
	bh, err := v1.NewHash(base)
	if err != nil || conv == "" || im.Annotations[AnnotationBaseName] == "" {
		return nil, fmt.Errorf("source %s: not an index kiki-imagepub published (annotations %v)", from, im.Annotations)
	}
	res := &Result{
		Source:    im.Annotations[AnnotationBaseName] + "@" + base,
		Ref:       to.Digest(from.DigestStr()).String(),
		Tags:      append([]string{SourceTag(bh)}, tags...),
		Platforms: map[string]string{},
		Converter: conv,
	}
	for _, d := range im.Manifests {
		if d.Platform != nil {
			res.Platforms[d.Platform.String()] = d.Digest.String()
		}
	}
	start := time.Now()
	if err := remote.WriteIndex(to.Tag(res.Tags[0]), idx, ropts...); err != nil {
		return nil, fmt.Errorf("push %s: %w", to.Tag(res.Tags[0]), err)
	}
	if err := tagAll(to, tags, idx, ropts); err != nil {
		return nil, err
	}
	log.Info("copied", "from", from, "ref", res.Ref, "tags", res.Tags, "took", time.Since(start).Round(time.Millisecond))
	res.Pushed = true
	return res, nil
}

// publishedIndex is an index Dest already holds for the source.
type publishedIndex struct {
	idx       v1.ImageIndex
	digest    v1.Hash
	platforms map[string]string
}

// published returns the index at tag if it was converted from the source
// index src by converter conv, and holds every platform in want. Any
// failure to read it means it is not.
func published(tag name.Tag, src v1.Hash, conv string, want []v1.Platform, ropts []remote.Option) (publishedIndex, bool) {
	desc, err := remote.Get(tag, ropts...)
	if err != nil || !desc.MediaType.IsIndex() {
		return publishedIndex{}, false
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		return publishedIndex{}, false
	}
	im, err := idx.IndexManifest()
	if err != nil || im.Annotations[AnnotationBaseDigest] != src.String() || im.Annotations[AnnotationConverter] != conv {
		return publishedIndex{}, false
	}
	pub := publishedIndex{idx: idx, digest: desc.Digest, platforms: map[string]string{}}
	for _, p := range want {
		d, err := platformManifest(im, p)
		if err != nil {
			return publishedIndex{}, false
		}
		pub.platforms[p.String()] = d.Digest.String()
	}
	return pub, true
}

// platformManifest returns the image manifest for p in im: the same OS
// and architecture, and the same variant if p names one. Attestation
// manifests (unknown/unknown) never match.
func platformManifest(im *v1.IndexManifest, p v1.Platform) (v1.Descriptor, error) {
	for _, d := range im.Manifests {
		if d.Platform == nil || !d.MediaType.IsImage() {
			continue
		}
		if d.Platform.OS == p.OS && d.Platform.Architecture == p.Architecture &&
			(p.Variant == "" || d.Platform.Variant == p.Variant) {
			return d, nil
		}
	}
	return v1.Descriptor{}, fmt.Errorf("no %s image", p)
}

// checkTool fails unless path is erofs-utils MkfsVersion's tool.
func checkTool(ctx context.Context, path, tool string) error {
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --version: %w\n%s", path, err, out)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	if want := tool + " (erofs-utils) " + MkfsVersion; strings.TrimSpace(first) != want {
		return fmt.Errorf("%s is %q, want %q: another version may write other bytes for the same layer", path, strings.TrimSpace(first), want)
	}
	return nil
}

// converter converts one platform's image.
type converter struct {
	mkfs string
	fsck string
	dir  string
	log  *slog.Logger
}

// image converts every layer of src and returns the image of the native
// layers: src's config with their diff_ids, OCI media types and
// annotations ann.
func (c *converter) image(ctx context.Context, src v1.Image, ann map[string]string) (v1.Image, error) {
	m, err := src.Manifest()
	if err != nil {
		return nil, err
	}
	cf, err := src.ConfigFile()
	if err != nil {
		return nil, err
	}
	layers, err := src.Layers()
	if err != nil {
		return nil, err
	}
	if len(layers) != len(cf.RootFS.DiffIDs) || len(layers) != len(m.Layers) {
		return nil, fmt.Errorf("%d layers, %d diff_ids", len(m.Layers), len(cf.RootFS.DiffIDs))
	}
	out := &converted{layers: map[v1.Hash]*fileLayer{}}
	diffIDs := make([]v1.Hash, len(layers))
	descs := make([]v1.Descriptor, len(layers))
	for i, l := range layers {
		d := m.Layers[i]
		if len(d.URLs) > 0 || !d.MediaType.IsDistributable() || !isTarLayer(d.MediaType) {
			return nil, fmt.Errorf("layer %d (%s): media type %s: not a distributable tar layer", i, d.Digest, d.MediaType)
		}
		fl, err := c.layer(ctx, i, l, cf.RootFS.DiffIDs[i])
		if err != nil {
			return nil, fmt.Errorf("layer %d (%s): %w", i, d.Digest, err)
		}
		diffIDs[i] = fl.diffID
		out.layers[fl.digest] = fl
		descs[i] = v1.Descriptor{MediaType: LayerMediaType, Size: fl.size, Digest: fl.digest, Annotations: map[string]string{
			AnnotationLayerSourceDigest: d.Digest.String(),
			AnnotationLayerSourceDiffID: cf.RootFS.DiffIDs[i].String(),
		}}
	}
	raw, err := src.RawConfigFile()
	if err != nil {
		return nil, err
	}
	if out.config, err = replaceDiffIDs(raw, diffIDs); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	ch, size, err := v1.SHA256(bytes.NewReader(out.config))
	if err != nil {
		return nil, err
	}
	if out.manifest, err = json.Marshal(v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		Config:        v1.Descriptor{MediaType: types.OCIConfigJSON, Size: size, Digest: ch},
		Layers:        descs,
		Annotations:   ann,
	}); err != nil {
		return nil, err
	}
	return partial.CompressedToImage(out)
}

// replaceDiffIDs returns the config raw with rootfs.diff_ids replaced by
// ids and nothing else changed: fields go-containerregistry does not
// model are kept, which reading it into a v1.ConfigFile would drop. Keys
// come out sorted and compact, so it is a function of raw and ids.
func replaceDiffIDs(raw []byte, ids []v1.Hash) ([]byte, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	var rootfs map[string]json.RawMessage
	if err := json.Unmarshal(cfg["rootfs"], &rootfs); err != nil {
		return nil, fmt.Errorf("rootfs: %w", err)
	}
	var err error
	if rootfs["diff_ids"], err = marshal(ids); err != nil {
		return nil, err
	}
	if cfg["rootfs"], err = marshal(rootfs); err != nil {
		return nil, err
	}
	return marshal(cfg)
}

// marshal is json.Marshal without HTML escaping, which would rewrite the
// source's strings.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// converted is a converted image: its raw config and manifest, and its
// layers on disk. partial.CompressedToImage makes it a v1.Image.
type converted struct {
	config, manifest []byte
	layers           map[v1.Hash]*fileLayer
}

func (i *converted) RawConfigFile() ([]byte, error)      { return i.config, nil }
func (i *converted) MediaType() (types.MediaType, error) { return types.OCIManifestSchema1, nil }
func (i *converted) RawManifest() ([]byte, error)        { return i.manifest, nil }

func (i *converted) LayerByDigest(h v1.Hash) (partial.CompressedLayer, error) {
	if l, ok := i.layers[h]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("no layer %s", h)
}

// tarLayers are the layer media types Publish converts: those kiki pulls,
// but for native layers.
var tarLayers = []types.MediaType{types.DockerLayer, types.OCILayer, types.OCILayerZStd, types.DockerUncompressedLayer, types.OCIUncompressedLayer}

func isTarLayer(mt types.MediaType) bool { return slices.Contains(tarLayers, mt) }

// layer converts layer i, whose uncompressed tar is diffID, into a
// compressed native blob in c.dir.
func (c *converter) layer(ctx context.Context, i int, l v1.Layer, diffID v1.Hash) (*fileLayer, error) {
	start := time.Now()
	raw := filepath.Join(c.dir, fmt.Sprintf("%d.erofs", i))
	defer os.Remove(raw)
	if err := c.run(ctx, l, diffID, raw); err != nil {
		return nil, err
	}
	mkfsTook := time.Since(start)
	f, err := os.Open(raw)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if out, err := exec.CommandContext(ctx, c.fsck, "--extract", raw).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("fsck.erofs refuses mkfs.erofs's blob: %w\n%s", err, out)
	}
	fl, err := compress(f, filepath.Join(c.dir, fmt.Sprintf("%d.erofs.zst", i)))
	if err != nil {
		return nil, err
	}
	c.log.Info("layer", "index", i, "source_diff_id", diffID, "erofs_bytes", st.Size(), "zstd_bytes", fl.size,
		"digest", fl.digest, "mkfs", mkfsTook.Round(time.Millisecond), "took", time.Since(start).Round(time.Millisecond))
	return fl, nil
}

// run pipes l's uncompressed tar into mkfs.erofs, writing out, and checks
// the tar against diffID.
func (c *converter) run(ctx context.Context, l v1.Layer, diffID v1.Hash, out string) error {
	rc, err := l.Uncompressed()
	if err != nil {
		return err
	}
	defer rc.Close()
	args := append(append([]string{}, mkfsArgs...), "-U"+uuidOf(diffID), out)
	cmd := exec.CommandContext(ctx, c.mkfs, args...)
	cmd.Env = mkfsEnv()
	if err := asRoot(cmd); err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr, cmd.Stdout = &stderr, &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	h := sha256.New()
	tee := io.TeeReader(rc, h)
	_, copyErr := io.Copy(stdin, tee)
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("mkfs.erofs: %w\n%s", err, stderr.Bytes())
	}
	if copyErr != nil && !errors.Is(copyErr, syscall.EPIPE) {
		return fmt.Errorf("read layer: %w", copyErr)
	}
	// mkfs.erofs may stop at the tar's end-of-archive blocks; the rest
	// still counts toward the diff_id.
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("read layer: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != diffID.Hex {
		return fmt.Errorf("uncompressed tar is sha256:%s, not its diff_id %s", got, diffID)
	}
	return nil
}

// mkfsEnv is the environment without SOURCE_DATE_EPOCH, which mkfs.erofs
// would take as the build time and, by default, every file's time.
func mkfsEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SOURCE_DATE_EPOCH=") {
			env = append(env, kv)
		}
	}
	return env
}

// uuidOf is the filesystem UUID of the blob converted from diffID: its
// first 16 bytes.
func uuidOf(diffID v1.Hash) string {
	x := diffID.Hex
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}

// compress writes f, a native blob, zstd-compressed to path, and returns
// it as a layer.
func compress(f *os.File, path string) (*fileLayer, error) {
	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	zh, rh := sha256.New(), sha256.New()
	cw := &countWriter{w: io.MultiWriter(out, zh)}
	enc, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(enc, io.TeeReader(f, rh)); err != nil {
		enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	return &fileLayer{
		path:   path,
		size:   cw.n,
		digest: v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(zh.Sum(nil))},
		diffID: v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(rh.Sum(nil))},
	}, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// fileLayer is a compressed native blob on disk.
type fileLayer struct {
	path           string
	size           int64
	digest, diffID v1.Hash
}

var _ v1.Layer = (*fileLayer)(nil)

func (l *fileLayer) Digest() (v1.Hash, error)             { return l.digest, nil }
func (l *fileLayer) DiffID() (v1.Hash, error)             { return l.diffID, nil }
func (l *fileLayer) Size() (int64, error)                 { return l.size, nil }
func (l *fileLayer) MediaType() (types.MediaType, error)  { return LayerMediaType, nil }
func (l *fileLayer) Compressed() (io.ReadCloser, error)   { return os.Open(l.path) }
func (l *fileLayer) Uncompressed() (io.ReadCloser, error) { return uncompressed(l.path) }

func uncompressed(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := zstd.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &decoder{d: d, f: f}, nil
}

type decoder struct {
	d *zstd.Decoder
	f *os.File
}

func (d *decoder) Read(p []byte) (int, error) { return d.d.Read(p) }
func (d *decoder) Close() error {
	d.d.Close()
	return d.f.Close()
}
