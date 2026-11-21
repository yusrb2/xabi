//go:build !linux

package xdg

import (
	"os"
	"path/filepath"
)

func defaultRuntimeDir(getenv func(string) string) string {
	if rt := absEnvDir(getenv("XDG_RUNTIME_DIR")); rt != "" {
		return filepath.Join(rt, appName)
	}
	return filepath.Join(os.TempDir(), appName)
}

func ensureRuntimeDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
