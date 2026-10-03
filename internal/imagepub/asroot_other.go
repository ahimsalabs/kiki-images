//go:build !linux

package imagepub

import (
	"errors"
	"os/exec"
)

// asRoot needs Linux: mkfs.erofs runs in a user namespace.
func asRoot(*exec.Cmd) error { return errors.New("converting needs Linux") }
