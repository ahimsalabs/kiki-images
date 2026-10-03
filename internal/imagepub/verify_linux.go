package imagepub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sys/unix"
)

// maxDiffs bounds the differences a failed layer reports.
const maxDiffs = 50

// Verify checks the index ref, which Publish made, against its source,
// which its annotations name: for every platform and layer, the native
// layer is loop-mounted read-only with the kernel's erofs and its entries
// compared with those of the source tar layer it was converted from
// (kind, mode, owner, mtime, content, symlink target, device, xattrs,
// whiteouts, opaque directories, hardlinks). It needs Linux, root and the
// erofs module, and room in workDir for the largest uncompressed layer.
func Verify(ctx context.Context, ref string, ropts []remote.Option, workDir string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("verify mounts each layer: run it as root")
	}
	d, err := name.NewDigest(ref)
	if err != nil {
		return fmt.Errorf("verify takes a digest reference: %w", err)
	}
	ropts = append(append([]remote.Option{}, ropts...), remote.WithContext(ctx))
	idx, err := remote.Index(d, ropts...)
	if err != nil {
		return err
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return err
	}
	base, err := name.NewRepository(im.Annotations[AnnotationBaseName])
	if err != nil || im.Annotations[AnnotationConverter] == "" {
		return fmt.Errorf("%s: not an index kiki-imagepub published (annotations %v)", ref, im.Annotations)
	}
	work, err := os.MkdirTemp(workDir, "kiki-imagepub-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for _, md := range im.Manifests {
		if md.Platform == nil || !md.MediaType.IsImage() {
			continue
		}
		img, err := idx.Image(md.Digest)
		if err != nil {
			return err
		}
		m, err := img.Manifest()
		if err != nil {
			return err
		}
		cf, err := img.ConfigFile()
		if err != nil {
			return err
		}
		if len(cf.RootFS.DiffIDs) != len(m.Layers) {
			return fmt.Errorf("%s: %d layers, %d diff_ids", md.Platform, len(m.Layers), len(cf.RootFS.DiffIDs))
		}
		lower := map[string]string{}
		for i, ld := range m.Layers {
			if ld.MediaType != LayerMediaType {
				return fmt.Errorf("%s: layer %d: media type %s", md.Platform, i, ld.MediaType)
			}
			srcDigest, srcDiffID := ld.Annotations[AnnotationLayerSourceDigest], ld.Annotations[AnnotationLayerSourceDiffID]
			start := time.Now()
			n, err := verifyLayer(ctx, img, ld.Digest, cf.RootFS.DiffIDs[i], base.Digest(srcDigest), srcDiffID, lower, work, ropts)
			if err != nil {
				return fmt.Errorf("%s: layer %d (%s, from %s): %w", md.Platform, i, ld.Digest, srcDigest, err)
			}
			log.Info("verified", "platform", md.Platform.String(), "layer", i, "entries", n,
				"took", time.Since(start).Round(time.Millisecond))
		}
	}
	return nil
}

// verifyLayer mounts the native layer digest of img, whose uncompressed
// blob is diffID, and compares it with the tar layer src, whose
// uncompressed stream is srcDiffID, and applies the layer to lower, the
// entries of the layers below it by kind. It returns the number of
// entries.
func verifyLayer(ctx context.Context, img v1.Image, digest, diffID v1.Hash, src name.Digest, srcDiffID string, lower map[string]string, work string, ropts []remote.Option) (int, error) {
	l, err := img.LayerByDigest(digest)
	if err != nil {
		return 0, err
	}
	blob := filepath.Join(work, "layer.erofs")
	defer os.Remove(blob)
	if err := writeChecked(l, blob, diffID); err != nil {
		return 0, err
	}
	sl, err := remote.Layer(src, ropts...)
	if err != nil {
		return 0, err
	}
	rc, err := sl.Uncompressed()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	h := sha256.New()
	tree, err := readTar(io.TeeReader(rc, h))
	if err != nil {
		return 0, fmt.Errorf("source tar: %w", err)
	}
	if _, err := io.Copy(h, rc); err != nil {
		return 0, err
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != srcDiffID {
		return 0, fmt.Errorf("source tar is %s, not the diff_id %s its annotation names", got, srcDiffID)
	}

	mnt := filepath.Join(work, "mnt")
	if err := os.Mkdir(mnt, 0o700); err != nil {
		return 0, err
	}
	defer os.Remove(mnt)
	if out, err := exec.CommandContext(ctx, "mount", "-t", "erofs", "-o", "ro,loop", blob, mnt).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("mount: %w\n%s", err, out)
	}
	defer func() { _ = exec.Command("umount", mnt).Run() }()
	mounted, err := readMount(mnt)
	if err != nil {
		return 0, err
	}
	d := append(tree.diff(mounted), tree.madeUpOver(lower)...)
	if n := len(d); n > 0 {
		more := ""
		if n > maxDiffs {
			more = fmt.Sprintf("\n... and %d more", n-maxDiffs)
			d = d[:maxDiffs]
		}
		return 0, fmt.Errorf("%d differences from the tar:\n%s%s", n, strings.Join(d, "\n"), more)
	}
	tree.applyTo(lower)
	return len(mounted), nil
}

// writeChecked writes l's uncompressed blob to path and checks it against
// diffID.
func writeChecked(l v1.Layer, path string, diffID v1.Hash) error {
	rc, err := l.Uncompressed()
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), rc); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != diffID.Hex {
		return fmt.Errorf("native blob is sha256:%s, not its diff_id %s", got, diffID)
	}
	return f.Close()
}

// readMount reads every entry below root, by its path relative to root.
func readMount(root string) (map[string]*got, error) {
	out := map[string]*got{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		g, err := readEntry(p)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		out[filepath.ToSlash(rel)] = g
		return nil
	})
	return out, err
}

func readEntry(p string) (*got, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return nil, err
	}
	g := &got{ino: st.Ino, node: node{
		mode:      int64(st.Mode & 0o7777),
		uid:       int(st.Uid),
		gid:       int(st.Gid),
		mtime:     st.Mtim.Sec,
		mtimeNsec: st.Mtim.Nsec,
	}}
	var err error
	if g.xattrs, err = xattrs(p); err != nil {
		return nil, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		g.kind = kindDir
	case unix.S_IFREG:
		g.kind = kindReg
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		g.size, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		h.Sum(g.sum[:0])
	case unix.S_IFLNK:
		g.kind = kindSymlink
		if g.link, err = os.Readlink(p); err != nil {
			return nil, err
		}
	case unix.S_IFCHR:
		g.kind, g.major, g.minor = kindChar, int64(unix.Major(st.Rdev)), int64(unix.Minor(st.Rdev))
		if st.Rdev == 0 {
			g.kind = kindWhiteout
		}
	case unix.S_IFBLK:
		g.kind, g.major, g.minor = kindBlock, int64(unix.Major(st.Rdev)), int64(unix.Minor(st.Rdev))
	case unix.S_IFIFO:
		g.kind = kindFifo
	default:
		return nil, fmt.Errorf("mode %#o", st.Mode)
	}
	return g, nil
}

func xattrs(p string) (map[string]string, error) {
	out := map[string]string{}
	buf := make([]byte, 64<<10)
	n, err := unix.Llistxattr(p, buf)
	if err != nil {
		return nil, fmt.Errorf("listxattr: %w", err)
	}
	for k := range bytes.SplitSeq(buf[:n], []byte{0}) {
		if len(k) == 0 {
			continue
		}
		v := make([]byte, 64<<10)
		m, err := unix.Lgetxattr(p, string(k), v)
		if err != nil {
			return nil, fmt.Errorf("getxattr %s: %w", k, err)
		}
		out[string(k)] = string(v[:m])
	}
	return out, nil
}
