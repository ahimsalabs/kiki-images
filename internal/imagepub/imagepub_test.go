package imagepub

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// mkfsOrSkip returns the pinned mkfs.erofs, or skips the test.
func mkfsOrSkip(t *testing.T) string {
	t.Helper()
	p := os.Getenv("KIKI_MKFS_EROFS")
	if p == "" {
		var err error
		if p, err = exec.LookPath("mkfs.erofs"); err != nil {
			t.Skip("mkfs.erofs not found (set KIKI_MKFS_EROFS; tools/build-erofs-utils.sh builds it)")
		}
	}
	if err := checkTool(t.Context(), p, "mkfs.erofs"); err != nil {
		t.Skip(err)
	}
	return p
}

type entry struct {
	name, body string
	typ        byte
	mode       int64
}

// tarLayer is a gzip layer of the entries, with no "./", as a Dockerfile
// step's layer has none.
func tarLayer(t *testing.T, ents ...entry) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, ModTime: time.Unix(1700000000, 0), Size: int64(len(e.body)), Format: tar.FormatPAX}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil })
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// sourceIndex pushes a two-platform index of two layers each, the upper
// one whiting out and replacing lower entries, and returns its reference.
func sourceIndex(t *testing.T, host string) string {
	t.Helper()
	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for _, arch := range []string{"amd64", "arm64"} {
		img, err := mutate.AppendLayers(empty.Image,
			tarLayer(t,
				entry{name: "etc/", typ: tar.TypeDir, mode: 0o755},
				entry{name: "etc/os-release", body: "ID=test-" + arch + "\n", typ: tar.TypeReg, mode: 0o644},
				entry{name: "opt/gone/", typ: tar.TypeDir, mode: 0o755},
				entry{name: "opt/gone/x", body: strings.Repeat("x", 10000), typ: tar.TypeReg, mode: 0o600},
				entry{name: "usr/bin/", typ: tar.TypeDir, mode: 0o755},
			),
			// The parents of what it changes, as docker's and
			// buildkit's layers hold them: else mkfs.erofs makes them
			// up over the lower ones, which Verify refuses.
			tarLayer(t,
				entry{name: "opt/", typ: tar.TypeDir, mode: 0o755},
				entry{name: "opt/.wh.gone", typ: tar.TypeReg},
				entry{name: "usr/", typ: tar.TypeDir, mode: 0o755},
				entry{name: "usr/bin/", typ: tar.TypeDir, mode: 0o755},
				entry{name: "usr/bin/tool", body: "#!/bin/sh\n", typ: tar.TypeReg, mode: 0o755},
				entry{name: "usr/bin/.wh..wh..opq", typ: tar.TypeReg},
			))
		if err != nil {
			t.Fatal(err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cf = cf.DeepCopy()
		cf.OS, cf.Architecture = "linux", arch
		cf.Config = v1.Config{Env: []string{"PATH=/usr/bin"}, User: "1000"}
		if img, err = mutate.ConfigFile(img, cf); err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Platform: &v1.Platform{OS: "linux", Architecture: arch}})
	}
	ref := host + "/upstream/img:v1"
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		t.Fatal(err)
	}
	return ref
}

// Publish converts every layer of every platform into a native layer kiki
// accepts, keeps each config but for its diff_ids, names the source in
// annotations, and is a function of the source: a second run finds it
// published, and a forced one pushes the same digest.
func TestPublish(t *testing.T) {
	mkfs := mkfsOrSkip(t)
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	src := sourceIndex(t, host)
	opts := Options{
		Source: src, Dest: host + "/images/img", Tags: []string{"latest"},
		Mkfs: mkfs, WorkDir: t.TempDir(), Logger: slog.New(slog.DiscardHandler),
	}
	res, err := Publish(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || len(res.Platforms) != 2 {
		t.Fatalf("result %+v", res)
	}
	srcDesc, err := remote.Get(mustRef(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if want := SourceTag(srcDesc.Digest); res.Tags[0] != want {
		t.Errorf("tags %q, want %s first", res.Tags, want)
	}
	pub := mustRef(t, res.Ref)
	desc, err := remote.Get(pub)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if im.MediaType != types.OCIImageIndex || im.Annotations[AnnotationBaseDigest] != srcDesc.Digest.String() ||
		im.Annotations[AnnotationBaseName] != host+"/upstream/img" || im.Annotations[AnnotationConverter] != ConverterID() {
		t.Errorf("index %s, annotations %v", im.MediaType, im.Annotations)
	}
	for _, tag := range res.Tags {
		d, err := remote.Head(mustRef(t, opts.Dest+":"+tag))
		if err != nil || d.Digest != desc.Digest {
			t.Errorf("tag %s: %v, %v", tag, d, err)
		}
	}
	srcIdx, err := srcDesc.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	srcIM, err := srcIdx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range im.Manifests {
		sd := srcIM.Manifests[i]
		if d.Platform.String() != sd.Platform.String() || res.Platforms[d.Platform.String()] != d.Digest.String() {
			t.Errorf("manifest %d: %s %s, source %s; result %v", i, d.Platform, d.Digest, sd.Platform, res.Platforms)
		}
		checkImage(t, idx, srcIdx, d, sd)
	}

	// As root, every layer mounts to its source tar's entries.
	if os.Geteuid() == 0 && os.Getenv("KIKI_IMAGES_NO_MOUNT") == "" {
		if err := Verify(t.Context(), res.Ref, nil, t.TempDir(), opts.Logger); err != nil {
			t.Errorf("verify: %v", err)
		}
	} else {
		t.Log("not root: Verify not run")
	}

	// A second run finds it; a forced one makes the same bytes.
	again, err := Publish(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.Pushed || again.Ref != res.Ref {
		t.Errorf("second run %+v, want %s found", again, res.Ref)
	}
	opts.Force = true
	forced, err := Publish(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !forced.Pushed || forced.Ref != res.Ref {
		t.Errorf("forced run %+v, want %s pushed again", forced, res.Ref)
	}

	// Staged there, it is copied to another repository unchanged, which
	// a check then finds.
	final := Options{Source: src, Dest: host + "/final/img", Tags: []string{"stable"}, CheckOnly: true, Logger: opts.Logger}
	if c, err := Publish(t.Context(), final); err != nil || c.Found || c.Ref != "" || c.Pushed || c.Current {
		t.Fatalf("check before copy: %+v, %v", c, err)
	}
	cp, err := Copy(t.Context(), res.Ref, final.Dest, []string{"stable"}, nil, opts.Logger)
	if err != nil {
		t.Fatal(err)
	}
	if want := final.Dest + "@" + desc.Digest.String(); cp.Ref != want || cp.Source != res.Source ||
		cp.Converter != res.Converter || len(cp.Platforms) != 2 {
		t.Errorf("copy %+v, want %s from %s", cp, want, res.Source)
	}
	for _, tag := range []string{res.Tags[0], "stable"} {
		d, err := remote.Head(mustRef(t, final.Dest+":"+tag))
		if err != nil || d.Digest != desc.Digest {
			t.Errorf("copied tag %s: %v, %v", tag, d, err)
		}
	}
	if c, err := Publish(t.Context(), final); err != nil || !c.Found || c.Ref != cp.Ref || c.Pushed || !c.Current {
		t.Errorf("check after copy: %+v, %v", c, err)
	}
	// A tag that has yet to move to it is not current.
	final.Tags = []string{"stable", "next"}
	if c, err := Publish(t.Context(), final); err != nil || !c.Found || c.Current {
		t.Errorf("check with a tag to move: %+v, %v", c, err)
	}
	// Copy takes only what Publish made, by digest.
	if _, err := Copy(t.Context(), src, final.Dest, nil, nil, opts.Logger); err == nil {
		t.Error("copy of a tag succeeded")
	}
	if _, err := Copy(t.Context(), mustRef(t, src).Context().Digest(srcDesc.Digest.String()).String(), final.Dest, nil, nil, opts.Logger); err == nil ||
		!strings.Contains(err.Error(), "not an index kiki-imagepub published") {
		t.Errorf("copy of the source: %v", err)
	}
}

// checkImage checks the published image d against its source sd.
func checkImage(t *testing.T, idx, srcIdx v1.ImageIndex, d, sd v1.Descriptor) {
	t.Helper()
	img, err := idx.Image(d.Digest)
	if err != nil {
		t.Fatal(err)
	}
	srcImg, err := srcIdx.Image(sd.Digest)
	if err != nil {
		t.Fatal(err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	sm, err := srcImg.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.MediaType != types.OCIManifestSchema1 || m.Config.MediaType != types.OCIConfigJSON ||
		m.Annotations[AnnotationBaseDigest] != sd.Digest.String() || m.Annotations[AnnotationSource] != sourceRepo {
		t.Errorf("%s: manifest %s, config %s, annotations %v", d.Platform, m.MediaType, m.Config.MediaType, m.Annotations)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	scf, err := srcImg.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if cf.Architecture != scf.Architecture || cf.Config.User != scf.Config.User || len(cf.Config.Env) != 1 ||
		len(cf.RootFS.DiffIDs) != len(scf.RootFS.DiffIDs) || len(cf.History) != len(scf.History) {
		t.Errorf("%s: config %+v, source %+v", d.Platform, cf, scf)
	}
	// The raw config is the source's but for its diff_ids.
	raw, err := img.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	sraw, err := srcImg.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := withoutDiffIDs(t, raw), withoutDiffIDs(t, sraw); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: config %v, source %v", d.Platform, got, want)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range layers {
		ld := m.Layers[i]
		if ld.MediaType != LayerMediaType || ld.Annotations[AnnotationLayerSourceDigest] != sm.Layers[i].Digest.String() ||
			ld.Annotations[AnnotationLayerSourceDiffID] != scf.RootFS.DiffIDs[i].String() {
			t.Errorf("%s: layer %d: %+v", d.Platform, i, ld)
		}
		rc, err := l.Uncompressed()
		if err != nil {
			t.Fatal(err)
		}
		blob, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		// An erofs: the superblock's magic at 1024.
		if len(blob) < 1028 || !bytes.Equal(blob[1024:1028], []byte{0xe2, 0xe1, 0xf5, 0xe0}) {
			t.Errorf("%s: layer %d: not an erofs", d.Platform, i)
		}
	}
}

func withoutDiffIDs(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m["rootfs"].(map[string]any), "diff_ids")
	return m
}

// replaceDiffIDs keeps every field but the diff_ids, those
// go-containerregistry does not model too, and is stable.
func TestReplaceDiffIDs(t *testing.T) {
	raw := `{"architecture":"arm64","config":{"Healthcheck":{"Test":["CMD","true"],"StartInterval":5},"Labels":{"a":"<b>"}},` +
		`"moby.buildkit.buildinfo.v1":"e30=","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:` + strings.Repeat("0", 64) + `"],"x":1}}`
	ids := []v1.Hash{{Algorithm: "sha256", Hex: strings.Repeat("1", 64)}, {Algorithm: "sha256", Hex: strings.Repeat("2", 64)}}
	got, err := replaceDiffIDs([]byte(raw), ids)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"architecture":"arm64","config":{"Healthcheck":{"Test":["CMD","true"],"StartInterval":5},"Labels":{"a":"<b>"}},` +
		`"moby.buildkit.buildinfo.v1":"e30=","os":"linux","rootfs":{"diff_ids":["sha256:` + strings.Repeat("1", 64) + `","sha256:` +
		strings.Repeat("2", 64) + `"],"type":"layers","x":1}}`
	if string(got) != want {
		t.Errorf("replaceDiffIDs:\n got %s\nwant %s", got, want)
	}
	for _, bad := range []string{`[]`, `{"rootfs":7}`, `{}`} {
		if _, err := replaceDiffIDs([]byte(bad), ids); err == nil {
			t.Errorf("replaceDiffIDs(%s) succeeded", bad)
		}
	}
}

func mustRef(t *testing.T, s string) name.Reference {
	t.Helper()
	r, err := name.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A source with a layer that is not a tar fails before anything is
// pushed.
func TestPublishRefusesNonTar(t *testing.T) {
	mkfs := mkfsOrSkip(t)
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	src := sourceIndex(t, host)
	desc, err := remote.Get(mustRef(t, src))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	img, err := idx.Image(im.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	native, err := mutate.Append(empty.Image, mutate.Addendum{Layer: ls[0], MediaType: "application/vnd.erofs.layer.v1"})
	if err != nil {
		t.Fatal(err)
	}
	bad := mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.OCIImageIndex),
		mutate.IndexAddendum{Add: native, Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}})
	ref := host + "/upstream/native:v1"
	if err := remote.WriteIndex(mustRef(t, ref), bad); err != nil {
		t.Fatal(err)
	}
	_, err = Publish(t.Context(), Options{
		Source: ref, Dest: host + "/images/native", Platforms: []v1.Platform{{OS: "linux", Architecture: "amd64"}},
		Mkfs: mkfs, WorkDir: t.TempDir(), Logger: slog.New(slog.DiscardHandler),
	})
	if err == nil || !strings.Contains(err.Error(), "not a distributable tar layer") {
		t.Fatalf("publish: %v", err)
	}
	if _, err := remote.Head(mustRef(t, host+"/images/native:latest")); err == nil {
		t.Error("pushed")
	}
}

func TestUUIDOf(t *testing.T) {
	h := v1.Hash{Algorithm: "sha256", Hex: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if got, want := uuidOf(h), "01234567-89ab-cdef-0123-456789abcdef"; got != want {
		t.Errorf("uuidOf = %s, want %s", got, want)
	}
}
