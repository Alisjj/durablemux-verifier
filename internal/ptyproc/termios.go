package ptyproc

import "golang.org/x/sys/unix"

func lflagOf(fd uintptr) (uint64, error) {
	t, err := unix.IoctlGetTermios(int(fd), ioctlGetTermios())
	if err != nil {
		return 0, err
	}
	return uint64(t.Lflag), nil
}
