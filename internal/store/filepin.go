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

	return file, fi, uint64(statT.Dev), statT.Ino, nil
}

// GetSectionReader verifies that the pinned file has not been altered, truncated, or replaced,
// and returns an io.SectionReader configured for concurrent pread reads.
func GetSectionReader(entry *ShareEntry) (*io.SectionReader, time.Time, error) {
	entry.Mu.Lock()
	f := entry.PinnedFile
	entry.Mu.Unlock()

	reader := &verifiedReaderAt{file: f, entry: entry}
	if err := reader.verify(); err != nil {
		return nil, time.Time{}, err
	}
	return io.NewSectionReader(reader, 0, entry.FileSize), time.Unix(0, entry.MtimeUnixNano), nil
}

type verifiedReaderAt struct {
	file  *os.File
	entry *ShareEntry
}

func (r *verifiedReaderAt) verify() error {
	if r.file == nil || r.entry.mutated.Load() {
		return ErrFileMutated
	}
	fi, err := r.file.Stat()
	if err != nil {
		r.entry.mutated.Store(true)
		return ErrFileMutated
	}
	statT, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || uint64(statT.Dev) != r.entry.DeviceID || statT.Ino != r.entry.Inode || fi.Size() != r.entry.FileSize || fi.ModTime().UnixNano() != r.entry.MtimeUnixNano || fileChangeTime(fi) != r.entry.CtimeUnixNano {
		r.entry.mutated.Store(true)
		return ErrFileMutated
	}
	return nil
}

func (r *verifiedReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.verify(); err != nil {
		return 0, err
	}
	n, err := r.file.ReadAt(p, offset)
	// 修改过的读取结果必须在返回给 HTTP 发送代码之前丢弃。
	if verifyErr := r.verify(); verifyErr != nil {
		return 0, verifyErr
	}
	return n, err
}
