package imagepub

import (
	"os"
	"os/exec"
	"syscall"
)

// asRoot runs cmd as uid and gid 0: as itself when root, else in a user
// namespace mapping this user to 0. mkfs.erofs gives a root directory it
// makes up (a tar without "./", as every Dockerfile step's is) its own
// uid and gid, which would otherwise be the publishing user's.
func asRoot(cmd *exec.Cmd) error {
	if os.Geteuid() == 0 && os.Getegid() == 0 {
		return nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
	}
	return nil
}
