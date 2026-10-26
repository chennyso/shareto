package store

import (
	"os"
	"syscall"
)

func fileChangeTime(fi os.FileInfo) int64 {
	stat := fi.Sys().(*syscall.Stat_t)
	return stat.Ctim.Sec*1e9 + stat.Ctim.Nsec
}
