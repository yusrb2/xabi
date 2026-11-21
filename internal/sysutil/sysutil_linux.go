//go:build linux

package sysutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func extraBinDirs() []string {
	return []string{"/usr/sbin", "/sbin", "/usr/local/sbin"}
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func defaultHostsPath() string {
	return "/etc/hosts"
}

func checkHostsFile(path string) HostsStatus {
	st := HostsStatus{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		st.Err = err
		return st
	}
	if info.IsDir() {
		st.Err = fmt.Errorf("%s is a directory", path)
		return st
	}
	st.Exists = true

	const (
		rOK = 4
		wOK = 2
	)
	if err := syscall.Access(path, rOK); err == nil {
		st.Readable = true
	} else {
		st.Err = err
	}
	if err := syscall.Access(path, wOK); err == nil {
		st.Writable = true
	}
	return st
}

func checkNetBindCap(sys System, binPath string) CapStatus {
	getcapBin, ok := sys.LookBinary("getcap")
	if !ok {
		return CapStatus{Supported: true, Err: errors.New("getcap binary not found")}
	}
	out, err := sys.Run(context.Background(), getcapBin, binPath)
	raw := strings.TrimSpace(string(out))
	if err != nil {
		return CapStatus{Supported: true, Raw: raw, Err: err}
	}
	hasCap := strings.Contains(raw, "cap_net_bind_service") && strings.Contains(raw, "ep")
	return CapStatus{Supported: true, Set: hasCap, Raw: raw}
}

func setNetBindCap(ctx context.Context, sys System, binPath string) error {
	setcapBin, ok := sys.LookBinary("setcap")
	if !ok {
		return errors.New("setcap binary not found (install libcap)")
	}
	out, err := sys.Run(ctx, setcapBin, "cap_net_bind_service=+ep", binPath)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
	return nil
}

func isRoot() bool {
	return os.Geteuid() == 0
}
