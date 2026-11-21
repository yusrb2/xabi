// Package xdg resolves and provisions XDG Base Directory paths for xabi.
package xdg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const appName = "xabi"

// Paths holds the resolved XDG directories used by xabi.
type Paths struct {
	ConfigDir  string
	DataDir    string
	RuntimeDir string
}

// Resolve computes the XDG directory paths from the current process environment.
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return ResolveWith(os.Getenv, home)
}

// ResolveWith computes XDG directory paths using the supplied environment lookup
// and fallback home directory. Relative XDG_* paths are ignored per the XDG spec.
func ResolveWith(getenv func(string) string, homeDir string) (Paths, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	configBase := absEnvDir(getenv("XDG_CONFIG_HOME"))
	dataBase := absEnvDir(getenv("XDG_DATA_HOME"))

	if configBase == "" || dataBase == "" {
		if homeDir == "" {
			homeDir = absEnvDir(getenv("HOME"))
		}
		if homeDir == "" {
			return Paths{}, errors.New("cannot resolve user home directory for XDG paths")
		}
		if configBase == "" {
			configBase = filepath.Join(homeDir, ".config")
		}
		if dataBase == "" {
			dataBase = filepath.Join(homeDir, ".local", "share")
		}
	}

	runtimeDir := defaultRuntimeDir(getenv)

	return Paths{
		ConfigDir:  filepath.Join(configBase, appName),
		DataDir:    filepath.Join(dataBase, appName),
		RuntimeDir: runtimeDir,
	}, nil
}

// Ensure creates the core config, data, and runtime directories with safe permissions.
func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", p.ConfigDir, err)
	}
	if err := os.MkdirAll(p.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory %s: %w", p.DataDir, err)
	}
	if err := ensureRuntimeDir(p.RuntimeDir); err != nil {
		return fmt.Errorf("create runtime directory %s: %w", p.RuntimeDir, err)
	}
	return nil
}

// ConfigFile returns the path to config.toml.
func (p Paths) ConfigFile() string {
	return filepath.Join(p.ConfigDir, "config.toml")
}

// NginxDir returns the directory holding user-space nginx configuration.
func (p Paths) NginxDir() string {
	return filepath.Join(p.ConfigDir, "nginx")
}

// NginxConf returns the main user-space nginx.conf path.
func (p Paths) NginxConf() string {
	return filepath.Join(p.NginxDir(), "nginx.conf")
}

// SitesDir returns the directory holding virtual host definitions.
func (p Paths) SitesDir() string {
	return filepath.Join(p.NginxDir(), "sites")
}

// SitesFile returns the path to the registered sites metadata file.
func (p Paths) SitesFile() string {
	return filepath.Join(p.ConfigDir, "sites.toml")
}

// CertsDir returns the directory holding generated local TLS certificates.
func (p Paths) CertsDir() string {
	return filepath.Join(p.ConfigDir, "certs")
}

// PHPDir returns the directory holding user-space PHP-FPM configuration.
func (p Paths) PHPDir() string {
	return filepath.Join(p.ConfigDir, "php")
}

// PHPDataDir returns the directory holding user-installed PHP runtimes per version.
func (p Paths) PHPDataDir() string {
	return filepath.Join(p.DataDir, "php")
}

// DBDir returns the user-space database data directory.
func (p Paths) DBDir() string {
	return filepath.Join(p.DataDir, "db")
}

// BinDir returns the user-space directory for managed symlinks (e.g. php).
func (p Paths) BinDir() string {
	return filepath.Join(p.DataDir, "bin")
}

// NginxTempDir returns the runtime directory for nginx temp buffers.
func (p Paths) NginxTempDir() string {
	return filepath.Join(p.RuntimeDir, "nginx")
}

// PIDFile returns the runtime PID file path for a managed service.
func (p Paths) PIDFile(service string) string {
	return filepath.Join(p.RuntimeDir, service+".pid")
}

// SocketFile returns the runtime Unix domain socket path for a managed service.
func (p Paths) SocketFile(service string) string {
	return filepath.Join(p.RuntimeDir, service+".sock")
}

// LogFile returns the runtime log file path for a managed service.
func (p Paths) LogFile(service string) string {
	return filepath.Join(p.RuntimeDir, service+".log")
}

func absEnvDir(val string) string {
	val = strings.TrimSpace(val)
	if val == "" || !filepath.IsAbs(val) {
		return ""
	}
	return filepath.Clean(val)
}
