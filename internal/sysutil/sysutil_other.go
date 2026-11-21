//go:build !linux && !windows

package sysutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func expandBinaryCandidates(candidates []string) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func extraBinDirs() []string {
	return nil
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
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
		return st
	}
	st.Exists = true

	if f, err := os.Open(path); err == nil {
		st.Readable = true
		_ = f.Close()
	} else {
		st.Err = err
	}
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		st.Writable = true
		_ = f.Close()
	}
	return st
}

func checkNetBindCap(_ System, _ string) CapStatus {
	return CapStatus{Supported: false}
}

func setNetBindCap(_ context.Context, _ System, _ string) error {
	return errors.New("setcap is only supported on Linux")
}

func isRoot() bool {
	return false
}

// HiddenSysProcAttr returns nil on non-Windows Unix systems.
func HiddenSysProcAttr() *syscall.SysProcAttr {
	return nil
}

func openURL(ctx context.Context, url string) error {
	cmd := exec.CommandContext(ctx, "open", url)
	return cmd.Start()
}
