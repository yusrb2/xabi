//go:build windows

package sysutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func expandBinaryCandidates(candidates []string) []string {
	seen := make(map[string]bool, len(candidates)*2)
	out := make([]string, 0, len(candidates)*2)
	for _, c := range candidates {
		if c == "" {
			continue
		}
		lower := strings.ToLower(c)
		if !strings.HasSuffix(lower, ".exe") {
			exe := c + ".exe"
			if !seen[exe] {
				seen[exe] = true
				out = append(out, exe)
			}
		}
		if !seen[c] {
			seen[c] = true
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
	if err != nil || info.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".exe" || ext == ".cmd" || ext == ".bat" || ext == ".com"
}

func defaultHostsPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
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

// HiddenSysProcAttr suppresses console popups for child tools on Windows.
func HiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
