//go:build !linux

package sysutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func extraBinDirs() []string {
	return nil
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func defaultHostsPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
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
