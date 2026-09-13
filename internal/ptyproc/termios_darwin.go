//go:build darwin

package ptyproc

import "golang.org/x/sys/unix"

func ioctlGetTermios() uint {
	return unix.TIOCGETA
}
