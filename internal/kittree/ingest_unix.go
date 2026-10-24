//go:build unix

package kittree

import (
	"io/fs"
	"syscall"
)

// openFlags opens a tree file without blocking on a FIFO and without
// following a symlink swapped in after the walk saw a regular file.
const openFlags = syscall.O_NONBLOCK | syscall.O_NOFOLLOW

// hardLinked reports whether the opened file has more than one link.
func hardLinked(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
