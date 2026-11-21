//go:build linux

package xdg

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func defaultRuntimeDir(getenv func(string) string) string {
	if rt := absEnvDir(getenv("XDG_RUNTIME_DIR")); rt != "" {
		return filepath.Join(rt, appName)
	}

	uid := os.Getuid()
	runUser := fmt.Sprintf("/run/user/%d", uid)
	if isOwnedDir(runUser, uid) {
		return filepath.Join(runUser, appName)
	}

	return filepath.Join("/tmp", fmt.Sprintf("%s-%d", appName, uid))
}

func ensureRuntimeDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}

	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", path)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	uid := uint32(os.Getuid())
	if st.Uid != uid {
		return fmt.Errorf("runtime directory %s owned by uid %d, expected %d", path, st.Uid, uid)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("chmod runtime directory %s: %w", path, err)
		}
	}
	return nil
}

func isOwnedDir(path string, uid int) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == uint32(uid)
}
