package providers

import (
	"os"
	"syscall"
)

// fileOwner is the uid that owns fi's file (-1 when the platform does not say).
func fileOwner(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// fileLinks is the number of hard links of fi's file (1 when the platform does not say).
func fileLinks(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
