package imagepub

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"slices"
	"strings"
	"testing"
	"time"
)

func tarOf(t *testing.T, hs ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, h := range hs {
		h.Format = tar.FormatPAX
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = strings.Repeat("x", int(h.Size))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The tar's entries, as mkfs.erofs --aufs converts them, against a mount
// that matches them and mounts that differ from them, one way each.
func TestCompare(t *testing.T) {
	mt := time.Unix(1700000000, 0)
	raw := tarOf(t,
		&tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mt},
		&tar.Header{Name: "etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3, ModTime: mt, Uid: 1, Gid: 2},
		&tar.Header{Name: "etc/passwd2", Typeflag: tar.TypeLink, Linkname: "etc/passwd", Mode: 0o644, ModTime: mt, Uid: 1, Gid: 2},
		&tar.Header{Name: "usr/bin/sh", Typeflag: tar.TypeSymlink, Linkname: "dash", Mode: 0o777, ModTime: mt},
		&tar.Header{Name: "usr/bin/ping", Typeflag: tar.TypeReg, Mode: 0o4755, ModTime: mt,
			PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "\x01\x02"}},
		&tar.Header{Name: "opt/.wh.gone", Typeflag: tar.TypeReg, ModTime: mt},
		&tar.Header{Name: "var/lib/.wh..wh..opq", Typeflag: tar.TypeReg, ModTime: mt},
		&tar.Header{Name: "./tmp/x", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1, ModTime: mt},
		&tar.Header{Name: "tmp/x", Typeflag: tar.TypeReg, Mode: 0o600, Size: 2, ModTime: mt.Add(123456789)},
		&tar.Header{Name: "srv/www/.wh..wh..opq", Typeflag: tar.TypeReg, ModTime: mt},
		&tar.Header{Name: "data/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mt,
			PAXRecords: map[string]string{"SCHILY.xattr.trusted.overlay.origin": "x"}},
	)
	tree, err := readTar(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := func(n int) (s [sha256.Size]byte) {
		return sha256.Sum256([]byte(strings.Repeat("x", n)))
	}
	dir := func() *got { return &got{node: node{kind: kindDir, mode: 0o755, xattrs: map[string]string{}}} }
	match := func() map[string]*got {
		m := map[string]*got{
			".": dir(), "etc": dir(), "usr": dir(), "usr/bin": dir(), "opt": dir(), "var": dir(), "var/lib": dir(), "tmp": dir(),
			"srv": dir(), "srv/www": dir(), "data": dir(),
			"etc/passwd":   {ino: 7, node: node{kind: kindReg, mode: 0o644, uid: 1, gid: 2, mtime: mt.Unix(), size: 3, sum: sum(3), xattrs: map[string]string{}}},
			"etc/passwd2":  {ino: 7, node: node{kind: kindReg, mode: 0o644, uid: 1, gid: 2, mtime: mt.Unix(), size: 3, sum: sum(3), xattrs: map[string]string{}}},
			"usr/bin/sh":   {node: node{kind: kindSymlink, mode: 0o777, mtime: mt.Unix(), link: "dash", xattrs: map[string]string{}}},
			"usr/bin/ping": {node: node{kind: kindReg, mode: 0o4755, mtime: mt.Unix(), sum: sum(0), xattrs: map[string]string{"security.capability": "\x01\x02"}}},
			"opt/gone":     {node: node{kind: kindWhiteout}},
			"tmp/x":        {node: node{kind: kindReg, mode: 0o600, mtime: mt.Unix(), mtimeNsec: 123456789, size: 2, sum: sum(2), xattrs: map[string]string{}}},
		}
		m["etc"].mtime = mt.Unix()
		m["var/lib"].xattrs[opaqueXattr] = "y"
		m["opt"].xattrs[originXattr] = ""
		m["srv/www"].xattrs[opaqueXattr] = "y"
		m["data"].mtime = mt.Unix()
		m["data"].xattrs[originXattr] = "x"
		return m
	}
	if d := tree.diff(match()); len(d) > 0 {
		t.Fatalf("matching mount differs:\n%s", strings.Join(d, "\n"))
	}
	for _, c := range []struct {
		name string
		edit func(map[string]*got)
		want string
	}{
		{"extra", func(m map[string]*got) { m["etc/shadow"] = &got{node: node{kind: kindReg}} }, "etc/shadow: file not in the tar"},
		{"missing", func(m map[string]*got) { delete(m, "usr/bin/sh") }, "usr/bin/sh: symlink missing"},
		{"mode", func(m map[string]*got) { m["usr/bin/ping"].mode = 0o755 }, "usr/bin/ping: mode 0755, want 04755"},
		{"owner", func(m map[string]*got) { m["etc/passwd"].uid = 0 }, "etc/passwd: owner 0:2, want 1:2"},
		{"mtime", func(m map[string]*got) { m["etc"].mtime = 0 }, "etc: mtime 0.000000000, want 1700000000.000000000"},
		{"content", func(m map[string]*got) { m["tmp/x"].sum = sum(1) }, "tmp/x: content"},
		{"target", func(m map[string]*got) { m["usr/bin/sh"].link = "bash" }, "usr/bin/sh: target bash, want dash"},
		{"xattr", func(m map[string]*got) { delete(m["usr/bin/ping"].xattrs, "security.capability") }, "usr/bin/ping: xattrs"},
		{"opaque", func(m map[string]*got) { delete(m["var/lib"].xattrs, opaqueXattr) }, "var/lib: opaque false, want true"},
		{"not opaque", func(m map[string]*got) { m["etc"].xattrs[opaqueXattr] = "y" }, "etc: opaque true, want false"},
		{"whiteout", func(m map[string]*got) { m["opt/gone"].kind = kindChar }, "opt/gone: char, want a whiteout"},
		{"origin", func(m map[string]*got) { delete(m["opt"].xattrs, originXattr) }, "opt: origin false"},
		{"no origin", func(m map[string]*got) { m["etc"].xattrs[originXattr] = "" }, "etc: origin true"},
		{"source origin", func(m map[string]*got) { delete(m["data"].xattrs, originXattr) }, `data: origin false "", want true "x"`},
		{"implied missing", func(m map[string]*got) { delete(m, "srv/www") }, "srv/www: implied directory missing"},
		{"nanoseconds", func(m map[string]*got) { m["tmp/x"].mtimeNsec = 0 }, "tmp/x: mtime 1700000000.000000000, want 1700000000.123456789"},
		{"hardlink", func(m map[string]*got) { m["etc/passwd2"].ino = 8 }, "etc/passwd2: not one inode with etc/passwd"},
		{"implied", func(m map[string]*got) { m["usr"] = &got{node: node{kind: kindReg}} }, "usr: file, want an implied directory"},
	} {
		m := match()
		c.edit(m)
		d := tree.diff(m)
		if !slices.ContainsFunc(d, func(s string) bool { return strings.HasPrefix(s, c.want) }) {
			t.Errorf("%s: differences %q, want one starting %q", c.name, d, c.want)
		}
	}
}

// A directory mkfs.erofs makes up is refused over a lower layer's, but for
// the root, and allowed where the layers below hold none.
func TestMadeUpOver(t *testing.T) {
	dir := func(n string) *tar.Header { return &tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o700} }
	file := func(n string) *tar.Header { return &tar.Header{Name: n, Typeflag: tar.TypeReg} }
	for _, c := range []struct {
		name   string
		layers [][]*tar.Header
		want   []string // the last layer's differences
	}{
		{"made up over lower", [][]*tar.Header{{dir("secure/")}, {file("secure/file")}}, []string{"secure"}},
		{"opaque only, over lower", [][]*tar.Header{{dir("a/"), dir("a/b/")}, {file("a/b/.wh..wh..opq")}}, []string{"a", "a/b"}},
		{"named", [][]*tar.Header{{dir("secure/")}, {dir("secure/"), file("secure/file")}}, nil},
		{"new", [][]*tar.Header{{dir("etc/")}, {file("srv/file")}}, nil},
		{"whited out below", [][]*tar.Header{{dir("secure/")}, {file(".wh.secure")}, {file("secure/file")}}, nil},
		{"opaque below", [][]*tar.Header{{dir("a/"), dir("a/b/")}, {dir("a/"), file("a/.wh..wh..opq")}, {file("a/b/file")}}, []string{"a"}},
		{"replaced by a file below", [][]*tar.Header{{dir("x/")}, {file("x")}, {file("x/y")}}, []string{"x"}},
		{"over a symlink", [][]*tar.Header{{dir("real/"), {Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "real"}}, {file("alias/file")}}, []string{"alias"}},
		{"symlink whited out below", [][]*tar.Header{{{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "real"}}, {file(".wh.alias")}, {file("alias/file")}}, nil},
	} {
		lower := map[string]string{}
		var got []string
		for i, hs := range c.layers {
			tree, err := readTar(bytes.NewReader(tarOf(t, hs...)))
			if err != nil {
				t.Fatalf("%s: layer %d: %v", c.name, i, err)
			}
			got = got[:0]
			for _, d := range tree.madeUpOver(lower) {
				p, _, _ := strings.Cut(d, ":")
				got = append(got, p)
			}
			tree.applyTo(lower)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: made up over lower: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestReadTarRefuses(t *testing.T) {
	raw := tarOf(t, &tar.Header{Name: "a", Typeflag: tar.TypeLink, Linkname: "b"})
	if _, err := readTar(bytes.NewReader(raw)); err == nil {
		t.Error("a hardlink to nothing was read")
	}
	// A whiteout and an entry of the same layer at or below the path it
	// names, in either order.
	file := func(n string) *tar.Header { return &tar.Header{Name: n, Typeflag: tar.TypeReg} }
	for name, hs := range map[string][]*tar.Header{
		"file then whiteout":    {file("etc/foo"), file("etc/.wh.foo")},
		"whiteout then file":    {file("etc/.wh.foo"), file("etc/foo")},
		"subtree then whiteout": {file("etc/foo/bar"), file("etc/.wh.foo")},
		"whiteout then subtree": {file("etc/.wh.foo"), file("etc/foo/bar")},
		"whiteout then opaque":  {file("etc/.wh.foo"), file("etc/foo/.wh..wh..opq")},
		"hardlink, other mode":  {{Name: "a", Typeflag: tar.TypeReg, Mode: 0o4755}, {Name: "b", Typeflag: tar.TypeLink, Linkname: "a", Mode: 0o755}},
		"hardlink, other owner": {file("a"), {Name: "b", Typeflag: tar.TypeLink, Linkname: "a", Uid: 1}},
	} {
		if _, err := readTar(bytes.NewReader(tarOf(t, hs...))); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	ok := tarOf(t, file("etc/.wh.foo"), file("etc/bar"), file("etc/.wh..wh..opq"), file("etc/.wh.baz"))
	if _, err := readTar(bytes.NewReader(ok)); err != nil {
		t.Errorf("whiteouts beside entries: %v", err)
	}
}
