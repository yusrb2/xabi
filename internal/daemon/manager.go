package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

// ProcInfo summarizes OS process metadata for a tracked PID.
type ProcInfo struct {
	PID     int
	Comm    string
	Cmdline string
	Alive   bool
}

type procOps interface {
	InspectPID(pid int) (ProcInfo, bool)
	TreeRSS(rootPID int) int64
	SendSignal(pid int, sig Signal) error
	ReapIfExited(pid int) bool
}

// DialFunc checks whether a network or Unix socket endpoint is accepting connections.
type DialFunc func(network, address string, timeout time.Duration) error

// StartBackgroundFunc launches a foreground daemon detached in the background (e.g. mariadbd) and returns its PID.
type StartBackgroundFunc func(bin string, args []string, logPath string) (int, error)

// LocalManager orchestrates nginx, php-fpm, and mariadb in user-space.
type LocalManager struct {
	Paths           xdg.Paths
	Cfg             config.Config
	Sys             sysutil.System
	Proc            procOps
	Dial            DialFunc
	StartBackground StartBackgroundFunc
	StopTimeout     time.Duration
	ReadyTimeout    time.Duration
	PollInterval    time.Duration
}

// NewManager creates a LocalManager with production defaults.
func NewManager(paths xdg.Paths, cfg config.Config, sys sysutil.System) *LocalManager {
	if sys == nil {
		sys = sysutil.OS{}
	}
	return &LocalManager{
		Paths:           paths,
		Cfg:             cfg,
		Sys:             sys,
		Proc:            defaultProcOps(),
		Dial:            defaultDial,
		StartBackground: defaultStartBackground,
		StopTimeout:     3 * time.Second,
		ReadyTimeout:    4 * time.Second,
		PollInterval:    10 * time.Millisecond,
	}
}

func defaultDial(network, address string, timeout time.Duration) error {
	conn, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

func defaultStartBackground(bin string, args []string, logPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachSysProcAttr()

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = logFile.Close()
	return pid, nil
}

// Up starts each requested service idempotently.
func (m *LocalManager) Up(ctx context.Context, services []string) ([]ActionResult, error) {
	if err := m.Paths.Ensure(); err != nil {
		return nil, err
	}

	results := make([]ActionResult, 0, len(services))
	for _, svc := range services {
		res, err := m.startOne(ctx, svc)
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// Down stops each requested service in reverse order and cleans runtime PID/socket files.
func (m *LocalManager) Down(ctx context.Context, services []string) ([]ActionResult, error) {
	results := make([]ActionResult, 0, len(services))
	for i := len(services) - 1; i >= 0; i-- {
		res, err := m.stopOne(ctx, services[i])
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// Restart stops and starts services, or sends a graceful reload signal when reloadOnly is true.
func (m *LocalManager) Restart(ctx context.Context, services []string, reloadOnly bool) ([]ActionResult, error) {
	if reloadOnly {
		results := make([]ActionResult, 0, len(services))
		for _, svc := range services {
			res, err := m.reloadOne(ctx, svc)
			if err != nil {
				return results, err
			}
			results = append(results, res)
		}
		return results, nil
	}

	if _, err := m.Down(ctx, services); err != nil {
		return nil, err
	}
	return m.Up(ctx, services)
}

// Status inspects all requested services, performing self-healing orphan PID recovery.
func (m *LocalManager) Status(ctx context.Context, services []string) ([]ServiceStatus, error) {
	out := make([]ServiceStatus, 0, len(services))
	for _, svc := range services {
		st, err := m.inspectService(ctx, svc)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func (m *LocalManager) inspectService(ctx context.Context, svc string) (ServiceStatus, error) {
	norm, err := NormalizeService(svc)
	if err != nil {
		return ServiceStatus{}, err
	}

	endpoint := m.serviceEndpoint(ctx, norm)
	pidPath := m.Paths.PIDFile(norm)
	st := ServiceStatus{
		Name:     norm,
		State:    StateStopped,
		Endpoint: endpoint,
	}

	pid, exists, valid := readPIDFile(pidPath)
	if !exists {
		return st, nil
	}
	if !valid {
		_ = os.Remove(pidPath)
		m.cleanupServiceSockets(ctx, norm)
		st.Recovered = true
		return st, nil
	}

	m.Proc.ReapIfExited(pid)
	info, alive := m.Proc.InspectPID(pid)
	if !alive || !MatchServiceProcess(norm, info.Comm, info.Cmdline) {
		_ = os.Remove(pidPath)
		m.cleanupServiceSockets(ctx, norm)
		st.Recovered = true
		return st, nil
	}

	st.State = StateRunning
	st.PID = pid
	st.MemoryRSS = m.Proc.TreeRSS(pid)
	return st, nil
}

func (m *LocalManager) startOne(ctx context.Context, svc string) (ActionResult, error) {
	st, err := m.inspectService(ctx, svc)
	if err != nil {
		return ActionResult{}, err
	}
	if st.State == StateRunning {
		return ActionResult{Name: st.Name, PID: st.PID, Changed: false, Note: "already running"}, nil
	}

	m.cleanupServiceSockets(ctx, st.Name)

	switch st.Name {
	case ServiceNginx:
		return m.startNginx(ctx)
	case ServicePHPFPM:
		return m.startPHPFPM(ctx)
	case ServiceMariaDB:
		return m.startMariaDB(ctx)
	default:
		return ActionResult{}, fmt.Errorf("unsupported service %q", st.Name)
	}
}

func (m *LocalManager) startNginx(ctx context.Context) (ActionResult, error) {
	if _, err := os.Stat(m.Paths.NginxConf()); err != nil {
		return ActionResult{}, fmt.Errorf("missing %s (run 'xabi init' first): %w", m.Paths.NginxConf(), err)
	}
	for _, sub := range []string{"client_body", "proxy", "fastcgi", "uwsgi", "scgi"} {
		if err := os.MkdirAll(filepath.Join(m.Paths.NginxTempDir(), sub), 0o755); err != nil {
			return ActionResult{}, fmt.Errorf("prepare nginx temp dir: %w", err)
		}
	}

	bin, ok := m.Sys.LookBinary("nginx")
	if !ok {
		return ActionResult{}, errors.New("nginx binary not found in PATH")
	}

	out, err := m.Sys.Run(ctx, bin, "-c", m.Paths.NginxConf(), "-p", m.Paths.RuntimeDir)
	if err != nil {
		return ActionResult{}, formatCmdFailure("start nginx", out, err)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(m.Cfg.Ports.HTTP))
	pid, err := m.waitReady(ctx, ServiceNginx, "tcp", addr, 0)
	if err != nil {
		return ActionResult{}, appendLogTail(err, m.Paths.LogFile(ServiceNginx))
	}
	return ActionResult{Name: ServiceNginx, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) startPHPFPM(ctx context.Context) (ActionResult, error) {
	ver, bin, ok := ResolvePHPVersion(ctx, m.Sys, m.Cfg)
	if !ok {
		return ActionResult{}, errors.New("php-fpm binary not found in PATH")
	}

	if err := os.MkdirAll(m.Paths.PHPDir(), 0o755); err != nil {
		return ActionResult{}, fmt.Errorf("create php config dir: %w", err)
	}

	confPath := PHPFPMConfPath(m.Paths, ver)
	sockPath := PHPFPMSocketPath(m.Paths, ver)
	if _, err := os.Stat(confPath); errors.Is(err, os.ErrNotExist) {
		rendered, err := RenderPHPFPMConf(PHPTemplateData{
			PIDFile:    filepath.ToSlash(m.Paths.PIDFile(ServicePHPFPM)),
			ErrorLog:   filepath.ToSlash(m.Paths.LogFile(ServicePHPFPM)),
			SocketFile: filepath.ToSlash(sockPath),
		})
		if err != nil {
			return ActionResult{}, fmt.Errorf("render php-fpm.conf: %w", err)
		}
		if err := sysutil.WriteFileAtomic(confPath, []byte(rendered), 0o644); err != nil {
			return ActionResult{}, fmt.Errorf("write %s: %w", confPath, err)
		}
	}

	out, err := m.Sys.Run(ctx, bin, "-y", confPath, "-D")
	if err != nil {
		return ActionResult{}, formatCmdFailure("start php-fpm", out, err)
	}

	pid, err := m.waitReady(ctx, ServicePHPFPM, "unix", sockPath, 0)
	if err != nil {
		return ActionResult{}, appendLogTail(err, m.Paths.LogFile(ServicePHPFPM))
	}
	return ActionResult{Name: ServicePHPFPM, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) startMariaDB(ctx context.Context) (ActionResult, error) {
	dbDir := m.Paths.DBDir()
	if _, err := os.Stat(filepath.Join(dbDir, "mysql")); err != nil {
		return ActionResult{}, fmt.Errorf("mariadb data dir not initialized at %s (run 'xabi init' first)", dbDir)
	}

	bin, ok := m.Sys.LookBinary("mariadbd", "mysqld")
	if !ok {
		return ActionResult{}, errors.New("mariadbd/mysqld binary not found in PATH")
	}

	sockPath := m.Paths.SocketFile("mysql")
	pidPath := m.Paths.PIDFile(ServiceMariaDB)
	logPath := m.Paths.LogFile(ServiceMariaDB)

	args := []string{
		"--datadir=" + dbDir,
		"--socket=" + sockPath,
		"--pid-file=" + pidPath,
		"--port=" + strconv.Itoa(m.Cfg.Ports.DB),
		"--log-error=" + logPath,
	}

	spawnedPID, err := m.StartBackground(bin, args, logPath)
	if err != nil {
		return ActionResult{}, fmt.Errorf("start mariadb: %w", err)
	}

	pid, err := m.waitReady(ctx, ServiceMariaDB, "unix", sockPath, spawnedPID)
	if err != nil {
		return ActionResult{}, appendLogTail(err, logPath)
	}
	return ActionResult{Name: ServiceMariaDB, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) stopOne(ctx context.Context, svc string) (ActionResult, error) {
	st, err := m.inspectService(ctx, svc)
	if err != nil {
		return ActionResult{}, err
	}
	if st.State == StateStopped {
		m.cleanupServiceSockets(ctx, st.Name)
		return ActionResult{Name: st.Name, Changed: false, Note: "already stopped"}, nil
	}

	if err := m.Proc.SendSignal(st.PID, SigTerm); err != nil {
		if _, alive := m.Proc.InspectPID(st.PID); alive {
			return ActionResult{}, fmt.Errorf("send SIGTERM to %s (pid %d): %w", st.Name, st.PID, err)
		}
	}

	if !m.waitUntilExited(ctx, st.PID, m.StopTimeout) {
		if err := m.Proc.SendSignal(st.PID, SigKill); err != nil {
			if _, alive := m.Proc.InspectPID(st.PID); alive {
				return ActionResult{}, fmt.Errorf("send SIGKILL to %s (pid %d): %w", st.Name, st.PID, err)
			}
		}
		if !m.waitUntilExited(ctx, st.PID, 500*time.Millisecond) {
			return ActionResult{}, fmt.Errorf("%s (pid %d) failed to exit after SIGKILL", st.Name, st.PID)
		}
	}

	_ = os.Remove(m.Paths.PIDFile(st.Name))
	m.cleanupServiceSockets(ctx, st.Name)
	return ActionResult{Name: st.Name, PID: st.PID, Changed: true, Note: "stopped"}, nil
}

func (m *LocalManager) reloadOne(ctx context.Context, svc string) (ActionResult, error) {
	st, err := m.inspectService(ctx, svc)
	if err != nil {
		return ActionResult{}, err
	}
	if st.State == StateStopped {
		return m.startOne(ctx, st.Name)
	}

	sig := SigHup
	if st.Name == ServicePHPFPM {
		sig = SigUsr2
	}
	if err := m.Proc.SendSignal(st.PID, sig); err != nil {
		return ActionResult{}, fmt.Errorf("reload %s (pid %d, %s): %w", st.Name, st.PID, sig, err)
	}
	return ActionResult{Name: st.Name, PID: st.PID, Changed: true, Note: "reloaded (" + sig.String() + ")"}, nil
}

func (m *LocalManager) waitReady(ctx context.Context, svc, network, address string, knownPID int) (int, error) {
	deadline := time.Now().Add(m.ReadyTimeout)
	pidPath := m.Paths.PIDFile(svc)

	for {
		if knownPID > 0 {
			m.Proc.ReapIfExited(knownPID)
			if _, alive := m.Proc.InspectPID(knownPID); !alive {
				return 0, fmt.Errorf("%s exited before becoming ready", svc)
			}
		}

		pid := knownPID
		if filePID, _, valid := readPIDFile(pidPath); valid {
			pid = filePID
		}

		if pid > 0 {
			info, alive := m.Proc.InspectPID(pid)
			if alive && MatchServiceProcess(svc, info.Comm, info.Cmdline) {
				if err := m.Dial(network, address, 20*time.Millisecond); err == nil {
					return pid, nil
				}
			}
		}

		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timed out waiting for %s readiness on %s:%s", svc, network, address)
		}

		timer := time.NewTimer(m.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *LocalManager) waitUntilExited(ctx context.Context, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		m.Proc.ReapIfExited(pid)
		if _, alive := m.Proc.InspectPID(pid); !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		timer := time.NewTimer(m.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (m *LocalManager) serviceEndpoint(ctx context.Context, svc string) string {
	switch svc {
	case ServiceNginx:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.HTTP)
	case ServicePHPFPM:
		if m.Cfg.PHPDefault == "" || m.Cfg.PHPDefault == "system" {
			if matches, err := filepath.Glob(filepath.Join(m.Paths.RuntimeDir, "php-*.sock")); err == nil && len(matches) > 0 {
				return "unix:" + filepath.ToSlash(matches[0])
			}
		}
		ver, _, _ := ResolvePHPVersion(ctx, m.Sys, m.Cfg)
		return "unix:" + filepath.ToSlash(PHPFPMSocketPath(m.Paths, ver))
	case ServiceMariaDB:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.DB)
	default:
		return "-"
	}
}

func (m *LocalManager) cleanupServiceSockets(ctx context.Context, svc string) {
	switch svc {
	case ServicePHPFPM:
		ver, _, _ := ResolvePHPVersion(ctx, m.Sys, m.Cfg)
		_ = os.Remove(PHPFPMSocketPath(m.Paths, ver))
	case ServiceMariaDB:
		_ = os.Remove(m.Paths.SocketFile("mysql"))
	}
}

func readPIDFile(path string) (pid int, exists bool, valid bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, false
	}
	raw := strings.TrimSpace(string(data))
	p, err := strconv.Atoi(raw)
	if err != nil || p <= 0 {
		return 0, true, false
	}
	return p, true, true
}

func formatCmdFailure(action string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return fmt.Errorf("%s: %s: %w", action, msg, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func appendLogTail(err error, logPath string) error {
	data, readErr := os.ReadFile(logPath)
	if readErr != nil || len(data) == 0 {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return fmt.Errorf("%w\n%s", err, strings.Join(lines, "\n"))
}
