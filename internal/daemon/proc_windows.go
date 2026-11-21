//go:build windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
)

var (
	kernel32DLL                    = syscall.NewLazyDLL("kernel32.dll")
	psapiDLL                       = syscall.NewLazyDLL("psapi.dll")
	procWaitForSingleObject        = kernel32DLL.NewProc("WaitForSingleObject")
	procGetExitCodeProcess         = kernel32DLL.NewProc("GetExitCodeProcess")
	procQueryFullProcessImageNameW = kernel32DLL.NewProc("QueryFullProcessImageNameW")
	procGetProcessMemoryInfo       = psapiDLL.NewProc("GetProcessMemoryInfo")
	procK32GetProcessMemoryInfo    = kernel32DLL.NewProc("K32GetProcessMemoryInfo")
)

const (
	winProcessQueryInfo        = 0x0400
	winProcessQueryLimitedInfo = 0x1000
	winProcessVMRead           = 0x0010
	winProcessTerminate        = 0x0001
	winSynchronize             = 0x00100000
	winWaitObject0             = 0x00000000
	winWaitTimeout             = 0x00000102
	winStillActive             = 259
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type windowsProcOps struct{}

func defaultProcOps() ProcOps {
	return windowsProcOps{}
}

func (windowsProcOps) InspectPID(pid int) (ProcInfo, bool) {
	if pid <= 0 {
		return ProcInfo{}, false
	}

	h, err := syscall.OpenProcess(winProcessQueryLimitedInfo|winSynchronize, false, uint32(pid))
	if err != nil {
		return ProcInfo{}, false
	}
	defer syscall.CloseHandle(h)

	waitRes, _, _ := procWaitForSingleObject.Call(uintptr(h), 0)
	if waitRes == winWaitObject0 {
		return ProcInfo{}, false
	}

	var exitCode uint32
	r, _, _ := procGetExitCodeProcess.Call(uintptr(h), uintptr(unsafe.Pointer(&exitCode)))
	if r == 0 || (exitCode != winStillActive && waitRes != winWaitTimeout) {
		return ProcInfo{}, false
	}

	var buf [1024]uint16
	size := uint32(len(buf))
	imgRes, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	var exePath, comm string
	if imgRes != 0 && size > 0 {
		exePath = syscall.UTF16ToString(buf[:size])
		comm = filepath.Base(exePath)
	}

	return ProcInfo{
		PID:     pid,
		Comm:    comm,
		Cmdline: exePath,
		Alive:   true,
	}, true
}

func (windowsProcOps) TreeRSS(rootPID int) int64 {
	if rootPID <= 0 {
		return 0
	}

	pids := collectProcessTree(rootPID)
	var total int64
	for _, pid := range pids {
		total += readWindowsProcessRSS(pid)
	}
	return total
}

func readWindowsProcessRSS(pid int) int64 {
	h, err := syscall.OpenProcess(winProcessQueryInfo|winProcessVMRead, false, uint32(pid))
	if err != nil {
		h, err = syscall.OpenProcess(winProcessQueryLimitedInfo, false, uint32(pid))
		if err != nil {
			return 0
		}
	}
	defer syscall.CloseHandle(h)

	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))

	fn := procGetProcessMemoryInfo
	if fn.Find() != nil {
		fn = procK32GetProcessMemoryInfo
	}
	r, _, _ := fn.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB))
	if r == 0 {
		return 0
	}
	return int64(pmc.WorkingSetSize)
}

func (windowsProcOps) SendSignal(pid int, sig Signal) error {
	switch sig {
	case SigTerm, SigKill:
		return terminateProcessTree(pid)
	default:
		return fmt.Errorf("signal %s not supported natively on Windows", sig)
	}
}

func (windowsProcOps) ReapIfExited(_ int) bool {
	return false
}

func collectProcessTree(rootPID int) []int {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return []int{rootPID}
	}
	defer syscall.CloseHandle(snap)

	children := make(map[int][]int)
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err := syscall.Process32First(snap, &entry); err == nil {
		for {
			pid := int(entry.ProcessID)
			ppid := int(entry.ParentProcessID)
			if pid > 0 && pid != ppid {
				children[ppid] = append(children[ppid], pid)
			}
			if err := syscall.Process32Next(snap, &entry); err != nil {
				break
			}
		}
	}

	var out []int
	visited := make(map[int]bool)
	queue := []int{rootPID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		out = append(out, cur)
		queue = append(queue, children[cur]...)
	}
	return out
}

func terminateProcessTree(rootPID int) error {
	// Snapshot first, then terminate parent first so supervisor processes cannot respawn workers.
	pids := collectProcessTree(rootPID)
	var lastErr error
	for _, pid := range pids {
		h, err := syscall.OpenProcess(winProcessTerminate, false, uint32(pid))
		if err != nil {
			continue
		}
		if err := syscall.TerminateProcess(h, 1); err != nil {
			lastErr = err
		}
		_ = syscall.CloseHandle(h)
	}
	return lastErr
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
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
		return ActionResult{}, errors.New("nginx.exe not found in PATH")
	}

	// On Windows, nginx.exe stays attached unless launched detached with HideWindow.
	args := []string{"-c", filepath.ToSlash(m.Paths.NginxConf()), "-p", filepath.ToSlash(m.Paths.RuntimeDir)}
	logPath := m.Paths.LogFile(ServiceNginx)
	spawnedPID, err := m.StartBackground(bin, args, logPath, nil)
	if err != nil {
		return ActionResult{}, fmt.Errorf("start nginx: %w", err)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(m.Cfg.Ports.HTTP))
	pid, err := m.waitReady(ctx, ServiceNginx, "tcp", addr, spawnedPID)
	if err != nil {
		_ = terminateProcessTree(spawnedPID)
		return ActionResult{}, appendLogTail(err, logPath)
	}
	return ActionResult{Name: ServiceNginx, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) startPHPFPMPlatform(ctx context.Context) (ActionResult, error) {
	_, bin, ok := ResolvePHPVersion(ctx, m.Sys, m.Cfg)
	if !ok {
		return ActionResult{}, errors.New("php-cgi.exe not found in PATH")
	}

	logPath := m.Paths.LogFile(ServicePHPFPM)
	pidPath := m.Paths.PIDFile(ServicePHPFPM)
	extraEnv := []string{"PHP_FCGI_CHILDREN=4", "PHP_FCGI_MAX_REQUESTS=1000"}
	spawnedPID, err := m.StartBackground(bin, []string{"-b", config.WindowsPHPFastCGIAddr}, logPath, extraEnv)
	if err != nil {
		return ActionResult{}, fmt.Errorf("start php-cgi: %w", err)
	}

	if err := sysutil.WriteFileAtomic(pidPath, []byte(strconv.Itoa(spawnedPID)+"\n"), 0o644); err != nil {
		_ = terminateProcessTree(spawnedPID)
		return ActionResult{}, fmt.Errorf("write %s: %w", pidPath, err)
	}

	pid, err := m.waitReady(ctx, ServicePHPFPM, "tcp", config.WindowsPHPFastCGIAddr, spawnedPID)
	if err != nil {
		_ = terminateProcessTree(spawnedPID)
		_ = os.Remove(pidPath)
		return ActionResult{}, appendLogTail(err, logPath)
	}
	return ActionResult{Name: ServicePHPFPM, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) startMariaDBPlatform(ctx context.Context) (ActionResult, error) {
	dbDir := m.Paths.DBDir()
	if _, err := os.Stat(filepath.Join(dbDir, "mysql")); err != nil {
		return ActionResult{}, fmt.Errorf("mariadb data dir not initialized at %s (run 'xabi init' first)", dbDir)
	}

	bin, ok := m.Sys.LookBinary("mariadbd", "mysqld")
	if !ok {
		return ActionResult{}, errors.New("mariadbd.exe/mysqld.exe not found in PATH")
	}

	pidPath := m.Paths.PIDFile(ServiceMariaDB)
	logPath := m.Paths.LogFile(ServiceMariaDB)
	args := []string{
		"--datadir=" + dbDir,
		"--pid-file=" + pidPath,
		"--port=" + strconv.Itoa(m.Cfg.Ports.DB),
		"--log-error=" + logPath,
	}

	spawnedPID, err := m.StartBackground(bin, args, logPath, nil)
	if err != nil {
		return ActionResult{}, fmt.Errorf("start mariadb: %w", err)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(m.Cfg.Ports.DB))
	pid, err := m.waitReady(ctx, ServiceMariaDB, "tcp", addr, spawnedPID)
	if err != nil {
		_ = terminateProcessTree(spawnedPID)
		return ActionResult{}, appendLogTail(err, logPath)
	}
	return ActionResult{Name: ServiceMariaDB, PID: pid, Changed: true, Note: "started"}, nil
}

func (m *LocalManager) stopServicePlatform(ctx context.Context, st ServiceStatus) error {
	if st.Name == ServiceNginx {
		if bin, ok := m.Sys.LookBinary("nginx"); ok {
			_, _ = m.Sys.Run(ctx, bin, "-c", filepath.ToSlash(m.Paths.NginxConf()), "-p", filepath.ToSlash(m.Paths.RuntimeDir), "-s", "quit")
			if m.waitUntilExited(ctx, st.PID, 1500*time.Millisecond) {
				return nil
			}
		}
	}

	if err := m.Proc.SendSignal(st.PID, SigKill); err != nil {
		if _, alive := m.Proc.InspectPID(st.PID); alive {
			return fmt.Errorf("terminate %s (pid %d): %w", st.Name, st.PID, err)
		}
	}
	if !m.waitUntilExited(ctx, st.PID, m.StopTimeout) {
		return fmt.Errorf("%s (pid %d) failed to exit after termination", st.Name, st.PID)
	}
	return nil
}

func (m *LocalManager) reloadServicePlatform(ctx context.Context, st ServiceStatus) (ActionResult, error) {
	if st.Name == ServiceNginx {
		bin, ok := m.Sys.LookBinary("nginx")
		if !ok {
			return ActionResult{}, errors.New("nginx.exe not found in PATH")
		}
		out, err := m.Sys.Run(ctx, bin, "-c", filepath.ToSlash(m.Paths.NginxConf()), "-p", filepath.ToSlash(m.Paths.RuntimeDir), "-s", "reload")
		if err != nil {
			return ActionResult{}, formatCmdFailure("reload nginx", out, err)
		}
		return ActionResult{Name: st.Name, PID: st.PID, Changed: true, Note: "reloaded (nginx -s reload)"}, nil
	}

	if _, err := m.stopOne(ctx, st.Name); err != nil {
		return ActionResult{}, err
	}
	res, err := m.startOne(ctx, st.Name)
	if err != nil {
		return ActionResult{}, err
	}
	res.Note = "restarted"
	return res, nil
}

func (m *LocalManager) serviceEndpointPlatform(_ context.Context, svc string) string {
	switch svc {
	case ServiceNginx:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.HTTP)
	case ServicePHPFPM:
		return config.WindowsPHPFastCGIAddr
	case ServiceMariaDB:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.DB)
	default:
		return "-"
	}
}

func (m *LocalManager) cleanupServiceSocketsPlatform(_ context.Context, _ string) {}
