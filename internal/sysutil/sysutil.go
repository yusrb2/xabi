// Package sysutil provides fast, standard-library system probes and atomic file helpers.
package sysutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PortStatus describes whether a local TCP port is currently occupied.
type PortStatus struct {
	Port   int
	InUse  bool
	Detail string
}

// HostsStatus describes accessibility of the system hosts file.
type HostsStatus struct {
	Path     string
	Exists   bool
	Readable bool
	Writable bool
	Err      error
}

// CapStatus describes whether cap_net_bind_service is enabled on a binary.
type CapStatus struct {
	Supported bool
	Set       bool
	Raw       string
	Err       error
}

// System abstracts host interactions so commands remain fast and unit-testable.
type System interface {
	LookBinary(candidates ...string) (string, bool)
	EvalSymlinks(path string) (string, error)
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	CheckPort(port int, timeout time.Duration) PortStatus
	CheckHosts(path string) HostsStatus
	HostsPath() string
	CheckNetBindCap(binPath string) CapStatus
	SetNetBindCap(ctx context.Context, binPath string) error
	IsRoot() bool
}

// OS implements System using real host OS calls.
type OS struct{}

// LookBinary locates the first available executable among candidates in PATH or sbin dirs.
func (OS) LookBinary(candidates ...string) (string, bool) {
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			return p, true
		}
	}
	for _, dir := range extraBinDirs() {
		for _, name := range candidates {
			if name == "" {
				continue
			}
			full := filepath.Join(dir, name)
			if isExecutableFile(full) {
				return full, true
			}
		}
	}
	return "", false
}

// EvalSymlinks resolves symlinks for path.
func (OS) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// Run executes an external command and returns combined stdout/stderr output.
func (OS) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// CheckPort probes whether a local TCP port is already accepting connections or bound.
func (OS) CheckPort(port int, timeout time.Duration) PortStatus {
	return CheckPort(port, timeout)
}

// CheckHosts verifies read/write permissions on the given hosts file.
func (OS) CheckHosts(path string) HostsStatus {
	return checkHostsFile(path)
}

// HostsPath returns the default OS hosts file path.
func (OS) HostsPath() string {
	return defaultHostsPath()
}

// CheckNetBindCap checks whether cap_net_bind_service=+ep is set on binPath.
func (o OS) CheckNetBindCap(binPath string) CapStatus {
	return checkNetBindCap(o, binPath)
}

// SetNetBindCap grants cap_net_bind_service=+ep on binPath.
func (o OS) SetNetBindCap(ctx context.Context, binPath string) error {
	return setNetBindCap(ctx, o, binPath)
}

// IsRoot reports whether the current process is running as root (UID 0).
func (OS) IsRoot() bool {
	return isRoot()
}

// CheckPort performs a millisecond TCP loopback probe (and unprivileged Listen check for ports >= 1024).
func CheckPort(port int, timeout time.Duration) PortStatus {
	if timeout <= 0 {
		timeout = 50 * time.Millisecond
	}

	for _, host := range []string{"127.0.0.1", "::1"} {
		addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			_ = conn.Close()
			return PortStatus{
				Port:   port,
				InUse:  true,
				Detail: fmt.Sprintf("listening on %s", addr),
			}
		}
	}

	// For non-privileged ports we can also verify loopback and wildcard bind availability directly.
	if port >= 1024 {
		for _, bindAddr := range []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf(":%d", port)} {
			ln, err := net.Listen("tcp", bindAddr)
			if err != nil {
				return PortStatus{
					Port:   port,
					InUse:  true,
					Detail: err.Error(),
				}
			}
			_ = ln.Close()
		}
	}

	return PortStatus{Port: port, InUse: false}
}

// WriteFileAtomic writes data to a temporary file in path's directory and renames it into place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".xabi-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// DirWritable reports whether dir exists, is a directory, and accepts new files.
func DirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".xabi-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// IsPermissionErr reports whether err represents an OS permission denial.
func IsPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}
