//go:build unix

package kittree

import (
	"io/fs"
	"syscall"
)

// openFlags opens a tree file without blocking on a FIFO swapped in after the
// walk saw a regular file. (os.Root follows symlinks whatever the flags; the
// SameFile check after the open refuses one.)
const openFlags = syscall.O_NONBLOCK

// hardLinked reports whether the opened file has more than one link.
func hardLinked(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
