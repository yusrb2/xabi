//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yusrb2/xabi/internal/sysutil"
)

type linuxProcOps struct{}

func defaultProcOps() ProcOps {
	return linuxProcOps{}
}

func (linuxProcOps) InspectPID(pid int) (ProcInfo, bool) {
	if pid <= 0 {
		return ProcInfo{}, false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return ProcInfo{}, false
	}

	statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcInfo{}, false
	}
	st, err := ParseProcStat(statBytes)
	if err != nil || st.State == 'Z' || st.State == 'X' {
		return ProcInfo{}, false
	}

	cmdlineBytes, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	cmdline := strings.ReplaceAll(string(cmdlineBytes), "\x00", " ")

	return ProcInfo{
		PID:     pid,
		Comm:    st.Comm,
		Cmdline: strings.TrimSpace(cmdline),
		Alive:   true,
	}, true
}

func (linuxProcOps) TreeRSS(rootPID int) int64 {
	if rootPID <= 0 {
		return 0
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return readProcRSS(rootPID)
	}

	children := make(map[int][]int)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		statBytes, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		st, err := ParseProcStat(statBytes)
		if err != nil || st.State == 'Z' || st.State == 'X' {
			continue
		}
		children[st.PPID] = append(children[st.PPID], pid)
	}

	var total int64
	visited := make(map[int]bool)
	queue := []int{rootPID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		total += readProcRSS(cur)
		queue = append(queue, children[cur]...)
	}
	return total
}

func readProcRSS(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	rss, err := ParseProcStatusRSS(data)
	if err != nil {
		return 0
	}
	return rss
}

func (linuxProcOps) SendSignal(pid int, sig Signal) error {
	var sysSig syscall.Signal
	switch sig {
	case SigTerm:
		sysSig = syscall.SIGTERM
	case SigKill:
		sysSig = syscall.SIGKILL
	case SigHup:
		sysSig = syscall.SIGHUP
	case SigUsr2:
		sysSig = syscall.SIGUSR2
	default:
		return fmt.Errorf("unsupported signal %v", sig)
	}
	return syscall.Kill(pid, sysSig)
}

func (linuxProcOps) ReapIfExited(pid int) bool {
	if pid <= 0 {
		return false
	}
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	return err == nil && wpid == pid
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func (m *LocalManager) startNginxPlatform(ctx context.Context) (ActionResult, error) {
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

func (m *LocalManager) startPHPFPMPlatform(ctx context.Context) (ActionResult, error) {
	ver, bin, ok := ResolvePHPWithDiscover(ctx, m.Paths, m.Sys, m.Cfg.PHPDefault, m.DiscoverPHP)
	if !ok {
		return ActionResult{}, errors.New("php-fpm binary not found in PATH")
	}

	sockPath := PHPFPMSocketPath(m.Paths, ver)
	pid, err := m.startLinuxPHPPool(ctx, ServicePHPFPM, ver, bin, sockPath)
	if err != nil {
		return ActionResult{}, err
	}

	// Keep default unix:$XDG_RUNTIME_DIR/xabi/php.sock pointing at the active versioned socket.
	defaultSock := m.Paths.SocketFile("php")
	if sockPath != defaultSock {
		_ = os.Remove(defaultSock)
		_ = os.Symlink(filepath.Base(sockPath), defaultSock)
	}

	for _, extraVer := range ExtraPHPVersions(m.Paths, ver) {
		extraBin := bin
		if _, resolved, found := ResolvePHPWithDiscover(ctx, m.Paths, m.Sys, extraVer, m.DiscoverPHP); found {
			extraBin = resolved
		}
		extraSock := PHPFPMSocketPath(m.Paths, extraVer)
		if _, err := m.startLinuxPHPPool(ctx, ServicePHPFPM+"-"+extraVer, extraVer, extraBin, extraSock); err != nil {
			return ActionResult{}, err
		}
	}

	return ActionResult{Name: ServicePHPFPM, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) startLinuxPHPPool(ctx context.Context, pidName, ver, bin, sockPath string) (int, error) {
	if err := os.MkdirAll(m.Paths.PHPDir(), 0o755); err != nil {
		return 0, fmt.Errorf("create php config dir: %w", err)
	}

	confPath := filepath.Join(m.Paths.PHPDir(), "php-fpm.conf")
	if pidName != ServicePHPFPM {
		confPath = PHPFPMConfPath(m.Paths, ver)
	}
	rendered, err := RenderPHPFPMConf(PHPTemplateData{
		PIDFile:    filepath.ToSlash(m.Paths.PIDFile(pidName)),
		ErrorLog:   filepath.ToSlash(m.Paths.LogFile(pidName)),
		SocketFile: filepath.ToSlash(sockPath),
	})
	if err != nil {
		return 0, fmt.Errorf("render php-fpm.conf: %w", err)
	}
	existing, statErr := os.ReadFile(confPath)
	// Always keep main php-fpm.conf synchronized if active PHP version changed; preserve custom extra confs unless missing.
	if errors.Is(statErr, os.ErrNotExist) || (pidName == ServicePHPFPM && !strings.Contains(string(existing), filepath.ToSlash(sockPath))) {
		if err := sysutil.WriteFileAtomic(confPath, []byte(rendered), 0o644); err != nil {
			return 0, fmt.Errorf("write %s: %w", confPath, err)
		}
	}

	out, err := m.Sys.Run(ctx, bin, "-y", confPath, "-D")
	if err != nil {
		return 0, formatCmdFailure("start "+pidName, out, err)
	}

	pid, err := m.waitReady(ctx, pidName, "unix", sockPath, 0)
	if err != nil {
		return 0, appendLogTail(err, m.Paths.LogFile(pidName))
	}
	return pid, nil
}

func (m *LocalManager) startMariaDBPlatform(ctx context.Context) (ActionResult, error) {
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

	spawnedPID, err := m.StartBackground(bin, args, logPath, nil)
	if err != nil {
		return ActionResult{}, fmt.Errorf("start mariadb: %w", err)
	}

	pid, err := m.waitReady(ctx, ServiceMariaDB, "unix", sockPath, spawnedPID)
	if err != nil {
		return ActionResult{}, appendLogTail(err, logPath)
	}
	return ActionResult{Name: ServiceMariaDB, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) stopServicePlatform(ctx context.Context, st ServiceStatus) error {
	if err := m.stopLinuxPID(ctx, st.Name, st.PID); err != nil {
		return err
	}
	if st.Name == ServicePHPFPM {
		for _, pidFile := range m.extraPHPPIDFiles() {
			if pid, _, valid := readPIDFile(pidFile); valid {
				if info, alive := m.Proc.InspectPID(pid); alive && m.matchesProcess(ServicePHPFPM, info) {
					_ = m.stopLinuxPID(ctx, filepath.Base(pidFile), pid)
				}
			}
			_ = os.Remove(pidFile)
		}
	}
	return nil
}

func (m *LocalManager) stopLinuxPID(ctx context.Context, name string, pid int) error {
	if err := m.Proc.SendSignal(pid, SigTerm); err != nil {
		if _, alive := m.Proc.InspectPID(pid); alive {
			return fmt.Errorf("send SIGTERM to %s (pid %d): %w", name, pid, err)
		}
	}

	if !m.waitUntilExited(ctx, pid, m.StopTimeout) {
		if err := m.Proc.SendSignal(pid, SigKill); err != nil {
			if _, alive := m.Proc.InspectPID(pid); alive {
				return fmt.Errorf("send SIGKILL to %s (pid %d): %w", name, pid, err)
			}
		}
		if !m.waitUntilExited(ctx, pid, 500*time.Millisecond) {
			return fmt.Errorf("%s (pid %d) failed to exit after SIGKILL", name, pid)
		}
	}
	return nil
}

func (m *LocalManager) reloadServicePlatform(_ context.Context, st ServiceStatus) (ActionResult, error) {
	sig := SigHup
	if st.Name == ServicePHPFPM {
		sig = SigUsr2
		for _, pidFile := range m.extraPHPPIDFiles() {
			if pid, _, valid := readPIDFile(pidFile); valid {
				if info, alive := m.Proc.InspectPID(pid); alive && m.matchesProcess(ServicePHPFPM, info) {
					_ = m.Proc.SendSignal(pid, sig)
				}
			}
		}
	}
	if err := m.Proc.SendSignal(st.PID, sig); err != nil {
		return ActionResult{}, fmt.Errorf("reload %s (pid %d, %s): %w", st.Name, st.PID, sig, err)
	}
	return ActionResult{Name: st.Name, PID: st.PID, Changed: true, Note: "reloaded (" + sig.String() + ")"}, nil
}

func (m *LocalManager) extraPHPPIDFiles() []string {
	matches, err := filepath.Glob(filepath.Join(m.Paths.RuntimeDir, "php-fpm-*.pid"))
	if err != nil {
		return nil
	}
	return matches
}

func (m *LocalManager) serviceEndpointPlatform(ctx context.Context, svc string) string {
	switch svc {
	case ServiceNginx:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.HTTP)
	case ServicePHPFPM:
		if m.Cfg.PHPDefault == "" || m.Cfg.PHPDefault == "system" {
			if matches, err := filepath.Glob(filepath.Join(m.Paths.RuntimeDir, "php*.sock")); err == nil && len(matches) > 0 {
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

func (m *LocalManager) cleanupServiceSocketsPlatform(ctx context.Context, svc string) {
	switch svc {
	case ServicePHPFPM:
		_ = os.Remove(m.Paths.SocketFile("php"))
		ver, _, _ := ResolvePHPVersionIn(ctx, m.Paths, m.Sys, m.Cfg.PHPDefault)
		_ = os.Remove(PHPFPMSocketPath(m.Paths, ver))
		for _, extraVer := range ExtraPHPVersions(m.Paths, ver) {
			_ = os.Remove(PHPFPMSocketPath(m.Paths, extraVer))
		}
	case ServiceMariaDB:
		_ = os.Remove(m.Paths.SocketFile("mysql"))
	}
}
