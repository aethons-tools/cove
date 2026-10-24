//go:build !unix

package kittree

import "io/fs"

// openFlags adds nothing where the platform has no FIFO or O_NOFOLLOW.
const openFlags = 0

// hardLinked cannot be told from fs.FileInfo here; the walk's type checks
// still hold.
func hardLinked(fs.FileInfo) bool { return false }
