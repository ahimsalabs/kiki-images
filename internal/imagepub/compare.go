package imagepub

import (
	"archive/tar"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
)

// Entry kinds a layer holds, as Compare reads them from a tar and from a
// mounted native layer.
const (
	kindDir      = "dir"
	kindReg      = "file"
	kindSymlink  = "symlink"
	kindChar     = "char"
	kindBlock    = "block"
	kindFifo     = "fifo"
	kindWhiteout = "whiteout" // overlayfs's: a 0:0 character device
)

// The xattrs mkfs.erofs --aufs sets on directories: opaqueXattr ("y") on
// one whose tar holds .wh..wh..opq in it, and originXattr (empty) on one
// it puts a whiteout in, so that overlayfs does not show the whiteout
// (kernel commit b79e05aaa166).
const (
	opaqueXattr = "trusted.overlay.opaque"
	originXattr = "trusted.overlay.origin"
)

// node is one entry of a layer, as its tar says it should be.
type node struct {
	kind         string
	mode         int64 // permission, setuid, setgid and sticky bits
	uid, gid     int
	mtime        int64 // seconds
	mtimeNsec    int64
	size         int64
	sum          [sha256.Size]byte
	link         string // a symlink's target
	major, minor int64
	xattrs       map[string]string
	// linkOf is the path a hardlink entry names; the mounted layer must
	// give both one inode.
	linkOf string
}

// layerTree is what a tar layer converts to: every entry by its path ("."
// for the root), and the directories that must carry opaqueXattr and
// originXattr.
type layerTree struct {
	nodes          map[string]*node
	opaque, origin map[string]bool
}

// readTar reads a tar layer as mkfs.erofs --tar=f --aufs converts it:
// .wh.<name> becomes a whiteout <name> and its directory's originXattr,
// .wh..wh..opq its directory's opaqueXattr, a later entry replaces an
// earlier one at the same path, and a hardlink is its target.
//
// A whiteout hides only lower layers' entries, but mkfs.erofs gives it
// the path it names in the layer too: a layer that both whites out a path
// and holds an entry at or below it, in either order, has no faithful
// conversion, and readTar refuses it.
func readTar(r io.Reader) (*layerTree, error) {
	t := &layerTree{nodes: map[string]*node{}, opaque: map[string]bool{}, origin: map[string]bool{}}
	// held: every path the layer has an entry at or below; whiteouts:
	// every path it whites out.
	held, whiteouts := map[string]bool{".": true}, map[string]bool{}
	hold := func(p string) error {
		for q := p; q != "."; q = path.Dir(q) {
			if whiteouts[q] {
				return fmt.Errorf("%s: in the layer, which also whites out %s", p, q)
			}
			held[q] = true
		}
		return nil
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return t, nil
		}
		if err != nil {
			return nil, err
		}
		p := cleanPath(h.Name)
		dir, base := path.Split(p)
		dir = cleanPath(dir)
		if base == ".wh..wh..opq" {
			if err := hold(dir); err != nil {
				return nil, err
			}
			t.opaque[dir] = true
			continue
		}
		if name, ok := strings.CutPrefix(base, ".wh."); ok && p != "." {
			wp := path.Join(dir, name)
			if held[wp] {
				return nil, fmt.Errorf("%s: whites out %s, which the layer holds", p, wp)
			}
			if err := hold(dir); err != nil {
				return nil, err
			}
			whiteouts[wp] = true
			t.put(wp, &node{kind: kindWhiteout})
			// Set as the whiteout is read: an entry that later
			// replaces it leaves it set.
			t.origin[dir] = true
			continue
		}
		n := &node{
			mode:      h.Mode & 0o7777,
			uid:       h.Uid,
			gid:       h.Gid,
			mtime:     h.ModTime.Unix(),
			mtimeNsec: int64(h.ModTime.Nanosecond()),
			xattrs:    map[string]string{},
		}
		for k, v := range h.PAXRecords {
			if x, ok := strings.CutPrefix(k, "SCHILY.xattr."); ok {
				n.xattrs[x] = v
			}
		}
		switch h.Typeflag {
		case tar.TypeDir:
			n.kind = kindDir
		case tar.TypeReg, tar.TypeRegA:
			n.kind = kindReg
			hs := sha256.New()
			if n.size, err = io.Copy(hs, tr); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			hs.Sum(n.sum[:0])
		case tar.TypeSymlink:
			n.kind, n.link = kindSymlink, h.Linkname
		case tar.TypeChar:
			n.kind, n.major, n.minor = kindChar, h.Devmajor, h.Devminor
		case tar.TypeBlock:
			n.kind, n.major, n.minor = kindBlock, h.Devmajor, h.Devminor
		case tar.TypeFifo:
			n.kind = kindFifo
		case tar.TypeLink:
			target := cleanPath(h.Linkname)
			tn, ok := t.nodes[target]
			if !ok || tn.kind == kindDir || tn.kind == kindWhiteout {
				return nil, fmt.Errorf("%s: hardlink to %s, which the layer does not hold as a file", p, h.Linkname)
			}
			// The header's own metadata: a tar applier sets it on
			// the inode it shares with its target, where mkfs.erofs
			// keeps the target's.
			if n.mode != tn.mode || n.uid != tn.uid || n.gid != tn.gid || n.mtime != tn.mtime ||
				n.mtimeNsec != tn.mtimeNsec || !maps.Equal(n.xattrs, tn.xattrs) {
				return nil, fmt.Errorf("%s: hardlink to %s with other metadata than its target's", p, h.Linkname)
			}
			c := *tn
			c.linkOf = target
			n = &c
		default:
			return nil, fmt.Errorf("%s: tar entry type %q", p, h.Typeflag)
		}
		if err := hold(p); err != nil {
			return nil, err
		}
		t.put(p, n)
	}
}

// put sets p to n; a non-directory replacing a directory takes its
// subtree with it.
func (t *layerTree) put(p string, n *node) {
	if old, ok := t.nodes[p]; ok && old.kind == kindDir && n.kind != kindDir {
		for q := range t.nodes {
			if strings.HasPrefix(q, p+"/") {
				delete(t.nodes, q)
			}
		}
	}
	t.nodes[p] = n
}

// implied returns the directories the tar does not name but mkfs.erofs
// makes up: the root, the ancestors of its entries, and the directories
// it only makes opaque.
func (t *layerTree) implied() map[string]bool {
	dirs := map[string]bool{".": true}
	add := func(p string) {
		for p != "." && !dirs[p] {
			dirs[p] = true
			p = path.Dir(p)
		}
	}
	for p := range t.nodes {
		add(path.Dir(p))
	}
	for p := range t.opaque {
		add(p)
	}
	return dirs
}

// madeUpOver returns a difference for each directory but the root that
// mkfs.erofs makes up where lower, the entries of the layers below by
// kind, holds one. Over a lower directory, overlayfs takes the made-up
// mode, owner and time, where a tar applier keeps the lower ones; over a
// lower symlink, the directory replaces it, where a tar applier follows
// it; over any other entry, a tar applier fails. The root is exempt:
// kiki's merge takes a root mkfs.erofs made up as implicit.
func (t *layerTree) madeUpOver(lower map[string]string) []string {
	var d []string
	for _, p := range slices.Sorted(maps.Keys(t.implied())) {
		if _, named := t.nodes[p]; p == "." || named {
			continue
		}
		if k, ok := lower[p]; ok {
			d = append(d, fmt.Sprintf("%s: a directory made up over a lower layer's %s, which a tar applier would keep", p, k))
		}
	}
	return d
}

// applyTo applies the layer to lower, the entries of the layers below it
// by kind, as a merge does: its whiteouts and opaque directories remove
// lower entries, and its entries, directories made up included, replace
// them (a non-directory a directory's whole subtree).
func (t *layerTree) applyTo(lower map[string]string) {
	drop := func(p string, self bool) {
		if self {
			delete(lower, p)
		}
		for q := range lower {
			if p == "." || strings.HasPrefix(q, p+"/") {
				delete(lower, q)
			}
		}
	}
	for p, n := range t.nodes {
		if k, ok := lower[p]; ok && k == kindDir && n.kind != kindDir {
			drop(p, true)
		}
		if n.kind == kindWhiteout {
			delete(lower, p)
		}
	}
	for p := range t.opaque {
		drop(p, false)
	}
	for p := range t.implied() {
		lower[p] = kindDir
	}
	for p, n := range t.nodes {
		if n.kind != kindWhiteout {
			lower[p] = n.kind
		}
	}
}

// cleanPath is a tar entry's path relative to the layer root: "." for the
// root, no leading "./" or "/", no trailing "/".
func cleanPath(p string) string {
	p = path.Clean("/" + p)
	if p == "/" {
		return "."
	}
	return p[1:]
}

// got is one entry of a mounted layer.
type got struct {
	node
	ino uint64
}

// diff compares the mounted layer's entries, by path, with the tar's, and
// returns each difference, sorted.
func (t *layerTree) diff(mounted map[string]*got) []string {
	var d []string
	implied := t.implied()
	for _, p := range slices.Sorted(maps.Keys(mounted)) {
		g := mounted[p]
		want, ok := t.nodes[p]
		switch {
		case !ok && !implied[p]:
			d = append(d, fmt.Sprintf("%s: %s not in the tar", p, g.kind))
			continue
		case !ok:
			if g.kind != kindDir {
				d = append(d, fmt.Sprintf("%s: %s, want an implied directory", p, g.kind))
			}
		case want.kind == kindWhiteout:
			if g.kind != kindWhiteout {
				d = append(d, fmt.Sprintf("%s: %s, want a whiteout", p, g.kind))
			}
			continue
		default:
			d = append(d, compareNode(p, want, &g.node)...)
			if want.linkOf != "" {
				if o, ok := mounted[want.linkOf]; !ok || o.ino != g.ino {
					d = append(d, fmt.Sprintf("%s: not one inode with %s, which it hardlinks", p, want.linkOf))
				}
			}
		}
		if g.kind == kindDir {
			// What mkfs.erofs sets, or the tar's own xattrs carry.
			var src map[string]string
			if ok {
				src = want.xattrs
			}
			wantOpaque := t.opaque[p] || src[opaqueXattr] == "y"
			if got := g.xattrs[opaqueXattr] == "y"; got != wantOpaque {
				d = append(d, fmt.Sprintf("%s: opaque %v, want %v", p, got, wantOpaque))
			}
			wantV, wantOrigin := src[originXattr]
			if t.origin[p] {
				wantV, wantOrigin = "", true
			}
			if v, got := g.xattrs[originXattr]; got != wantOrigin || v != wantV {
				d = append(d, fmt.Sprintf("%s: origin %v %q, want %v %q", p, got, v, wantOrigin, wantV))
			}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(t.nodes)) {
		if _, ok := mounted[p]; !ok {
			d = append(d, fmt.Sprintf("%s: %s missing", p, t.nodes[p].kind))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(implied)) {
		if _, ok := mounted[p]; !ok {
			if _, named := t.nodes[p]; !named {
				d = append(d, fmt.Sprintf("%s: implied directory missing", p))
			}
		}
	}
	return d
}

func compareNode(p string, want, g *node) []string {
	var d []string
	f := func(what string, got, want any) {
		d = append(d, fmt.Sprintf("%s: %s %v, want %v", p, what, got, want))
	}
	if g.kind != want.kind {
		f("kind", g.kind, want.kind)
		return d
	}
	if g.mode != want.mode {
		f("mode", fmt.Sprintf("%#o", g.mode), fmt.Sprintf("%#o", want.mode))
	}
	if g.uid != want.uid || g.gid != want.gid {
		f("owner", fmt.Sprintf("%d:%d", g.uid, g.gid), fmt.Sprintf("%d:%d", want.uid, want.gid))
	}
	if g.mtime != want.mtime || g.mtimeNsec != want.mtimeNsec {
		f("mtime", fmt.Sprintf("%d.%09d", g.mtime, g.mtimeNsec), fmt.Sprintf("%d.%09d", want.mtime, want.mtimeNsec))
	}
	switch want.kind {
	case kindReg:
		if g.size != want.size || g.sum != want.sum {
			f("content", fmt.Sprintf("%d bytes sha256:%x", g.size, g.sum), fmt.Sprintf("%d bytes sha256:%x", want.size, want.sum))
		}
	case kindSymlink:
		if g.link != want.link {
			f("target", g.link, want.link)
		}
	case kindChar, kindBlock:
		if g.major != want.major || g.minor != want.minor {
			f("device", fmt.Sprintf("%d:%d", g.major, g.minor), fmt.Sprintf("%d:%d", want.major, want.minor))
		}
	}
	gx, wx := maps.Clone(g.xattrs), maps.Clone(want.xattrs)
	if g.kind == kindDir {
		// diff checks the ones mkfs.erofs sets.
		for _, x := range []map[string]string{gx, wx} {
			delete(x, opaqueXattr)
			delete(x, originXattr)
		}
	}
	if !maps.Equal(gx, wx) {
		f("xattrs", gx, wx)
	}
	return d
}
