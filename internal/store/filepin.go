package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// OpenAndPinFile resolves symlinks, opens the file using O_NOFOLLOW and O_CLOEXEC,
// verifies that it is a non-empty regular file, and returns the file descriptor along with metadata.
func OpenAndPinFile(rawPath string) (*os.File, os.FileInfo, uint64, uint64, error) {
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to get absolute path: %w", err)
	}

	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to resolve symlink: %w", err)
	}

	fd, err := unix.Open(realPath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("failed to open file securely: %w", err)
	}

	file := os.NewFile(uintptr(fd), realPath)
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, 0, 0, fmt.Errorf("failed to stat file: %w", err)
	}

	if !fi.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("only regular files can be shared")
	}

	if fi.Size() <= 0 {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("empty files cannot be shared")
	}

	statT, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		_ = file.Close()
		return nil, nil, 0, 0, errors.New("failed to retrieve syscall.Stat_t")
	}

	return file, fi, statT.Dev, statT.Ino, nil
}

// GetSectionReader verifies that the pinned file has not been altered, truncated, or replaced,
// and returns an io.SectionReader configured for concurrent pread reads.
func GetSectionReader(entry *ShareEntry) (*io.SectionReader, time.Time, error) {
	entry.Mu.Lock()
	f := entry.PinnedFile
	entry.Mu.Unlock()

	if f == nil {
		return nil, time.Time{}, ErrFileMutated
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, ErrFileMutated
	}

	statT, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, time.Time{}, ErrFileMutated
	}

	if statT.Dev != entry.DeviceID ||
		statT.Ino != entry.Inode ||
		fi.Size() != entry.FileSize ||
		fi.ModTime().UnixNano() != entry.MtimeUnixNano {
		return nil, time.Time{}, ErrFileMutated
	}

	return io.NewSectionReader(f, 0, entry.FileSize), fi.ModTime(), nil
}
