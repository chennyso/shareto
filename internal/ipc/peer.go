package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Unix socket 地址使用较短的等价路径，遵守内核 sockaddr_un 的长度限制。
func socketAddress(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(workingDir, absolute)
	if err != nil {
		return "", err
	}
	if len(relative) < len(absolute) {
		return relative, nil
	}
	return absolute, nil
}

func verifyPeerUID(conn net.Conn, expected uint32) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("IPC requires a Unix domain socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		uid, credentialErr = socketPeerUID(int(fd))
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return credentialErr
	}
	if uid != expected {
		return fmt.Errorf("IPC peer UID %d does not match user UID %d", uid, expected)
	}
	return nil
}
