// Package daemon manages user-space service processes, PID tracking, readiness probes, and OS inspection.
package daemon

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

// Canonical managed service names.
const (
	ServiceNginx   = "nginx"
	ServicePHPFPM  = "php-fpm"
	ServiceMariaDB = "mariadb"
)

// State represents the runtime state of a managed service.
type State string

const (
	StateRunning State = "RUNNING"
	StateStopped State = "STOPPED"
)

// Signal represents OS signals mapped per platform.
type Signal uint8

const (
	SigTerm Signal = iota
	SigKill
	SigHup
	SigUsr2
)

// String returns the canonical UNIX signal name.
func (s Signal) String() string {
	switch s {
	case SigTerm:
		return "SIGTERM"
	case SigKill:
		return "SIGKILL"
	case SigHup:
		return "SIGHUP"
	case SigUsr2:
		return "SIGUSR2"
	default:
		return "UNKNOWN"
	}
}

// ServiceStatus describes the live status of a single managed service.
type ServiceStatus struct {
	Name      string
	State     State
	PID       int
	Endpoint  string
	MemoryRSS int64
	Recovered bool
}

// ActionResult summarizes the outcome of a lifecycle command on one service.
type ActionResult struct {
	Name    string
	PID     int
	Changed bool
	Note    string
}

// Controller defines the process orchestration operations used by CLI commands.
type Controller interface {
	Up(ctx context.Context, services []string) ([]ActionResult, error)
	Down(ctx context.Context, services []string) ([]ActionResult, error)
	Restart(ctx context.Context, services []string, reloadOnly bool) ([]ActionResult, error)
	Status(ctx context.Context, services []string) ([]ServiceStatus, error)
}

// NormalizeService maps user aliases (e.g. "php", "php-cgi", "mysql", "mysqld") to canonical names.
func NormalizeService(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "nginx":
		return ServiceNginx, nil
	case "php-fpm", "php", "phpfpm", "php-cgi", "phpcgi":
		return ServicePHPFPM, nil
	case "mariadb", "mariadbd", "mysql", "mysqld":
		return ServiceMariaDB, nil
	default:
		return "", fmt.Errorf("unknown service %q (expected nginx, php-fpm, mariadb)", name)
	}
}

// ResolveServices validates a requested list or defaults to cfg.Services.
func ResolveServices(cfg config.Config, requested []string) ([]string, error) {
	raw := cfg.Services
	if len(requested) > 0 {
		raw = requested
	}
	if len(raw) == 0 {
		raw = []string{ServiceNginx, ServicePHPFPM, ServiceMariaDB}
	}

	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		norm, err := NormalizeService(r)
		if err != nil {
			return nil, err
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out, nil
}

// PHPBinaryCandidates returns platform-appropriate PHP FastCGI / FPM executable candidates.
func PHPBinaryCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{"php-cgi", "php-fpm"}
	}
	return []string{
		"php-fpm",
		"php-fpm8.4", "php-fpm8.3", "php-fpm8.2", "php-fpm8.1",
		"php-fpm84", "php-fpm83", "php-fpm82", "php-fpm81",
	}
}

// PHPTemplateData holds paths interpolated into user-space php-fpm.conf.
type PHPTemplateData struct {
	PIDFile    string
	ErrorLog   string
	SocketFile string
}

var phpFPMTmpl = template.Must(template.New("php-fpm.conf").Parse(`; Managed by xabi — user-space PHP-FPM configuration.

[global]
pid = {{.PIDFile}}
error_log = {{.ErrorLog}}
daemonize = yes

[www]
listen = {{.SocketFile}}
listen.mode = 0660
pm = dynamic
pm.max_children = 5
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 3
clear_env = no
`))

// RenderPHPFPMConf renders a minimal user-space php-fpm.conf.
func RenderPHPFPMConf(data PHPTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := phpFPMTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// PHPFPMConfPath returns the user-space php-fpm.conf path for a given PHP version tag.
func PHPFPMConfPath(paths xdg.Paths, version string) string {
	if version == "" || version == "system" {
		return filepath.Join(paths.PHPDir(), "php-fpm.conf")
	}
	return filepath.Join(paths.PHPDir(), "php-"+version+".conf")
}

// PHPFPMSocketPath returns the Unix domain socket path for a given PHP version tag.
func PHPFPMSocketPath(paths xdg.Paths, version string) string {
	if version == "" || version == "system" {
		return paths.SocketFile("php")
	}
	return paths.SocketFile("php-" + version)
}

// ResolvePHPVersion determines the active PHP version tag and binary path.
func ResolvePHPVersion(ctx context.Context, sys sysutil.System, cfg config.Config) (version string, binPath string, ok bool) {
	candidates := PHPBinaryCandidates()

	ver := strings.TrimSpace(cfg.PHPDefault)
	if ver != "" && ver != "system" && runtime.GOOS != "windows" {
		compact := strings.ReplaceAll(ver, ".", "")
		candidates = append([]string{"php-fpm" + ver, "php-fpm" + compact, "php" + ver + "-fpm"}, candidates...)
	}

	binPath, ok = sys.LookBinary(candidates...)
	if !ok {
		if ver == "" {
			ver = "system"
		}
		return ver, "", false
	}

	if ver != "" && ver != "system" {
		return ver, binPath, true
	}

	out, err := sys.Run(ctx, binPath, "-v")
	if err == nil {
		if detected := ParsePHPVersionOutput(string(out)); detected != "" {
			return detected, binPath, true
		}
	}
	return "system", binPath, true
}

// ParsePHPVersionOutput extracts major.minor from `php -v` / `php-fpm -v` output (e.g. "8.3").
func ParsePHPVersionOutput(out string) string {
	for _, field := range strings.Fields(out) {
		parts := strings.Split(field, ".")
		if len(parts) >= 2 && isDigits(parts[0]) && isDigits(parts[1]) {
			return parts[0] + "." + parts[1]
		}
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
