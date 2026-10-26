package store

import (
	"os"
	"syscall"
)

func fileChangeTime(fi os.FileInfo) int64 {
	stat := fi.Sys().(*syscall.Stat_t)
	return stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec
}
