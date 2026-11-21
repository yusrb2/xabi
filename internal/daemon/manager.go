package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/php"
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

// ProcOps abstracts OS-level process inspection, memory accounting, and signaling.
type ProcOps interface {
	InspectPID(pid int) (ProcInfo, bool)
	TreeRSS(rootPID int) int64
	SendSignal(pid int, sig Signal) error
	ReapIfExited(pid int) bool
}

// DialFunc checks whether a network or Unix socket endpoint is accepting connections.
type DialFunc func(network, address string, timeout time.Duration) error

// StartBackgroundFunc launches a daemon process in the background and returns its PID.
type StartBackgroundFunc func(bin string, args []string, logPath string, extraEnv []string) (int, error)

// LocalManager orchestrates nginx, php-fpm/php-cgi, and mariadb natively on Linux and Windows.
type LocalManager struct {
	Paths           xdg.Paths
	Cfg             config.Config
	Sys             sysutil.System
	Proc            ProcOps
	Dial            DialFunc
	StartBackground StartBackgroundFunc
	MatchProcess    func(service string, info ProcInfo) bool
	DiscoverPHP     func(context.Context, xdg.Paths, sysutil.System) []php.Installation
	StopTimeout     time.Duration
	ReadyTimeout    time.Duration
	PollInterval    time.Duration
}

// NewManager creates a LocalManager with production defaults.
func NewManager(paths xdg.Paths, cfg config.Config, sys sysutil.System) *LocalManager {
	if sys == nil {
		sys = sysutil.OS{BinDir: paths.BinDir()}
	}
	return &LocalManager{
		Paths:           paths,
		Cfg:             cfg,
		Sys:             sys,
		Proc:            defaultProcOps(),
		Dial:            defaultDial,
		StartBackground: defaultStartBackground,
		MatchProcess: func(service string, info ProcInfo) bool {
			return MatchServiceProcess(service, info.Comm, info.Cmdline)
		},
		DiscoverPHP:  php.Discover,
		StopTimeout:  3 * time.Second,
		ReadyTimeout: 4 * time.Second,
		PollInterval: 10 * time.Millisecond,
	}
}

func defaultDial(network, address string, timeout time.Duration) error {
	conn, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

func defaultStartBackground(bin string, args []string, logPath string, extraEnv []string) (int, error) {
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
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

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

// Restart stops and starts services, or reloads configuration when reloadOnly is true.
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

	endpoint := m.serviceEndpointPlatform(ctx, norm)
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
		m.cleanupServiceSocketsPlatform(ctx, norm)
		st.Recovered = true
		return st, nil
	}

	m.Proc.ReapIfExited(pid)
	info, alive := m.Proc.InspectPID(pid)
	if !alive || !m.matchesProcess(norm, info) {
		_ = os.Remove(pidPath)
		m.cleanupServiceSocketsPlatform(ctx, norm)
		st.Recovered = true
		return st, nil
	}

	st.State = StateRunning
	st.PID = pid
	st.MemoryRSS = m.Proc.TreeRSS(pid)
	return st, nil
}

func (m *LocalManager) matchesProcess(service string, info ProcInfo) bool {
	if m.MatchProcess != nil {
		return m.MatchProcess(service, info)
	}
	return MatchServiceProcess(service, info.Comm, info.Cmdline)
}

func (m *LocalManager) startOne(ctx context.Context, svc string) (ActionResult, error) {
	st, err := m.inspectService(ctx, svc)
	if err != nil {
		return ActionResult{}, err
	}
	if st.State == StateRunning {
		return ActionResult{Name: st.Name, PID: st.PID, Changed: false, Note: "already running"}, nil
	}

	m.cleanupServiceSocketsPlatform(ctx, st.Name)

	switch st.Name {
	case ServiceNginx:
		return m.startNginxPlatform(ctx)
	case ServicePHPFPM:
		return m.startPHPFPMPlatform(ctx)
	case ServiceMariaDB:
		return m.startMariaDBPlatform(ctx)
	default:
		return ActionResult{}, fmt.Errorf("unsupported service %q", st.Name)
	}
}

func (m *LocalManager) stopOne(ctx context.Context, svc string) (ActionResult, error) {
	st, err := m.inspectService(ctx, svc)
	if err != nil {
		return ActionResult{}, err
	}
	if st.State == StateStopped {
		m.cleanupServiceSocketsPlatform(ctx, st.Name)
		return ActionResult{Name: st.Name, Changed: false, Note: "already stopped"}, nil
	}

	if err := m.stopServicePlatform(ctx, st); err != nil {
		return ActionResult{}, err
	}

	_ = os.Remove(m.Paths.PIDFile(st.Name))
	m.cleanupServiceSocketsPlatform(ctx, st.Name)
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
	return m.reloadServicePlatform(ctx, st)
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
			if alive && m.matchesProcess(svc, info) {
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
