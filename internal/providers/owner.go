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
