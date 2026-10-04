//go:build linux

package ptyproc

import "golang.org/x/sys/unix"

func ioctlGetTermios() uint {
	return unix.TCGETS
}

func ioctlSetTermios() uint { return unix.TCSETS }
