// Command kiki-imagepub publishes an OCI image again with native EROFS
// layers, as internal/imagepub describes, and prints what it published as
// JSON. The publish workflow (.github/workflows/publish.yml) runs it for
// kiki's catalog images.
//
//	kiki-imagepub -source ghcr.io/boldsoftware/exeuntu:latest \
//	    -dest ghcr.io/ahimsalabs/kiki/exeuntu -tag latest
//
// With -check it only reports whether -dest has the source converted by
// this converter (JSON "found"). With -copy <repository@digest> it pushes
// an index it published elsewhere (a staging registry, where the workflow
// verified it) to -dest unchanged, tagged as -tag and its source tag say.
// With -verify <repository@digest>, run as root, it mounts every layer of
// an index it published and compares it with its source tar layer, entry
// for entry, and prints nothing.
//
// Credentials come from the Docker config (docker login), as kiki's own
// pulls do. Converting needs Linux, mkfs.erofs and fsck.erofs 1.9.3
// (tools/build-erofs-utils.sh) and, unless root, unprivileged user
// namespaces.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/ahimsalabs/kiki-images/internal/imagepub"
)

func main() {
	var (
		opts      imagepub.Options
		platforms = flag.String("platform", "linux/amd64,linux/arm64", "comma-separated platforms to convert")
		tags      = flag.String("tag", "", "comma-separated tags to point at the index, beside source-sha256-<hex>")
		converter = flag.Bool("converter", false, "print the converter id and exit")
		copyFrom  = flag.String("copy", "", "push this index, which kiki-imagepub published, to dest unchanged")
		verify    = flag.String("verify", "", "compare every layer of this index, which kiki-imagepub published, with its source (root)")
	)
	flag.StringVar(&opts.Source, "source", "", "the image to convert (a multi-platform index)")
	flag.StringVar(&opts.Dest, "dest", "", "the repository to push to")
	flag.StringVar(&opts.Mkfs, "mkfs", "", "mkfs.erofs to run (default: from $PATH)")
	flag.StringVar(&opts.Fsck, "fsck", "", "fsck.erofs to run (default: beside mkfs.erofs, else from $PATH)")
	flag.StringVar(&opts.WorkDir, "workdir", "", "directory for the converted layers (default: $TMPDIR)")
	flag.BoolVar(&opts.Force, "force", false, "convert and push even if dest has this source converted by this converter")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "convert and validate, push nothing")
	flag.BoolVar(&opts.CheckOnly, "check", false, "report whether dest has this source converted by this converter; convert nothing")
	flag.Parse()
	if *converter {
		fmt.Println(imagepub.ConverterID())
		return
	}
	if *verify != "" {
		if opts.Source != "" || opts.Dest != "" || *copyFrom != "" || flag.NArg() > 0 {
			usage()
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ropts := []remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain)}
		if err := imagepub.Verify(ctx, *verify, ropts, opts.WorkDir, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
			fatal(err)
		}
		return
	}
	if (opts.Source == "") == (*copyFrom == "") || opts.Dest == "" || flag.NArg() > 0 ||
		(*copyFrom != "" && (opts.CheckOnly || opts.DryRun || opts.Force)) {
		usage()
	}
	for p := range strings.SplitSeq(*platforms, ",") {
		pl, err := v1.ParsePlatform(p)
		if err != nil {
			fatal(err)
		}
		opts.Platforms = append(opts.Platforms, *pl)
	}
	if *tags != "" {
		opts.Tags = strings.Split(*tags, ",")
	}
	opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	opts.Remote = []remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var (
		res *imagepub.Result
		err error
	)
	if *copyFrom != "" {
		res, err = imagepub.Copy(ctx, *copyFrom, opts.Dest, opts.Tags, opts.Remote, opts.Logger)
	} else {
		res, err = imagepub.Publish(ctx, opts)
	}
	if err != nil {
		fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kiki-imagepub -source <ref> -dest <repository> [flags]\n"+
		"       kiki-imagepub -copy <repository@digest> -dest <repository> [-tag <tags>]\n"+
		"       kiki-imagepub -verify <repository@digest> [-workdir <dir>]")
	flag.PrintDefaults()
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "kiki-imagepub:", err)
	os.Exit(1)
}
