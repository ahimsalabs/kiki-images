//go:build !linux

package imagepub

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Verify needs Linux: it mounts each layer with the kernel's erofs.
func Verify(context.Context, string, []remote.Option, string, *slog.Logger) error {
	return errors.New("verify needs Linux")
}
