package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

// TestHelperDaemonProcess acts as a real background OS subprocess for cross-platform daemon tests.
func TestHelperDaemonProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if os.Getenv("HELPER_EXIT_NOW") == "1" {
		os.Exit(0)
	}

	if pidFile := os.Getenv("HELPER_PID_FILE"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	}
	if readyFile := os.Getenv("HELPER_READY_FILE"); readyFile != "" {
		_ = os.WriteFile(readyFile, []byte("ready\n"), 0o644)
	}

	// Stay alive until terminated by the parent test.
	select {
	case <-time.After(30 * time.Second):
	}
	os.Exit(0)
}

type stubProcEntry struct {
	info ProcInfo
	rss  int64
}

type fakeProcOps struct {
	mu           sync.Mutex
	procs        map[int]stubProcEntry
	signals      []Signal
	signalCount  map[int]int
	forceSurvive map[int]bool
}

func newFakeProcOps() *fakeProcOps {
	return &fakeProcOps{
		procs:        make(map[int]stubProcEntry),
		signalCount:  make(map[int]int),
		forceSurvive: make(map[int]bool),
	}
}

func (f *fakeProcOps) InspectPID(pid int) (ProcInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.procs[pid]
	if !ok || !e.info.Alive {
		return ProcInfo{}, false
	}
	return e.info, true
}

func (f *fakeProcOps) TreeRSS(rootPID int) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[rootPID].rss
}

func (f *fakeProcOps) SendSignal(pid int, sig Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, sig)
	f.signalCount[pid]++
	if sig == SigKill || (sig == SigTerm && !f.forceSurvive[pid]) {
		delete(f.procs, pid)
	}
	return nil
}

func (f *fakeProcOps) ReapIfExited(_ int) bool {
	return false
}

type fakeSys struct {
	bins  map[string]string
	calls [][]string
	onRun func(name string, args ...string) ([]byte, error)
}

func (f *fakeSys) LookBinary(candidates ...string) (string, bool) {
	for _, c := range candidates {
		if p, ok := f.bins[c]; ok {
			return p, true
		}
	}
	return "", false
}

func (f *fakeSys) EvalSymlinks(path string) (string, error) { return path, nil }
func (f *fakeSys) CheckPort(port int, _ time.Duration) sysutil.PortStatus {
	return sysutil.PortStatus{Port: port}
}
func (f *fakeSys) CheckHosts(path string) sysutil.HostsStatus      { return sysutil.HostsStatus{Path: path} }
func (f *fakeSys) HostsPath() string                               { return "/etc/hosts" }
func (f *fakeSys) CheckNetBindCap(_ string) sysutil.CapStatus      { return sysutil.CapStatus{} }
func (f *fakeSys) SetNetBindCap(_ context.Context, _ string) error { return nil }
func (f *fakeSys) OpenURL(_ context.Context, _ string) error       { return nil }
func (f *fakeSys) IsRoot() bool                                    { return false }
func (f *fakeSys) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.onRun != nil {
		return f.onRun(name, args...)
	}
	return nil, nil
}

func testXDGPaths(t *testing.T) xdg.Paths {
	t.Helper()
	base := t.TempDir()
	p := xdg.Paths{
		ConfigDir:  filepath.Join(base, "config", "xabi"),
		DataDir:    filepath.Join(base, "share", "xabi"),
		RuntimeDir: filepath.Join(base, "run", "xabi"),
	}
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	return p
}

func TestParseProcStatStatusAndMatching(t *testing.T) {
	statLine := []byte("12401 (nginx: worker process) S 12400 12400 12400 0 -1 4194304 100 0 0 0 1 0 0 0 20 0 1 0 99 1024 500")
	st, err := ParseProcStat(statLine)
	if err != nil {
		t.Fatalf("ParseProcStat failed: %v", err)
	}
	if st.PID != 12401 || st.Comm != "nginx: worker process" || st.State != 'S' || st.PPID != 12400 {
		t.Errorf("unexpected ProcStat: %+v", st)
	}

	zLine := []byte("55 (mariadbd) Z 1 55 55 0 -1")
	zst, err := ParseProcStat(zLine)
	if err != nil || zst.State != 'Z' {
		t.Fatalf("expected zombie state 'Z', got %+v (err=%v)", zst, err)
	}

	statusContent := []byte("Name:\tphp-fpm\nState:\tS (sleeping)\nVmPeak:\t   50000 kB\nVmRSS:\t   18432 kB\nThreads:\t1\n")
	rss, err := ParseProcStatusRSS(statusContent)
	if err != nil {
		t.Fatalf("ParseProcStatusRSS failed: %v", err)
	}
	if want := int64(18432 * 1024); rss != want {
		t.Errorf("VmRSS = %d, want %d", rss, want)
	}
	if got := FormatBytes(rss); got != "18.0 MiB" {
		t.Errorf("FormatBytes = %q, want %q", got, "18.0 MiB")
	}

	if !MatchServiceProcess(ServicePHPFPM, "php-cgi.exe", `C:\php\php-cgi.exe`) {
		t.Error("expected php-cgi.exe to match ServicePHPFPM")
	}
	if !MatchServiceProcess(ServiceNginx, "nginx.exe", `C:\nginx\nginx.exe`) {
		t.Error("expected nginx.exe to match ServiceNginx")
	}
	if MatchServiceProcess(ServiceNginx, "bash", "/bin/bash") {
		t.Error("expected bash not to match ServiceNginx")
	}
}

func TestOrphanPIDSelfHealingAndPIDReuseSafety(t *testing.T) {
	paths := testXDGPaths(t)
	cfg := config.Default()
	proc := newFakeProcOps()

	if err := os.WriteFile(paths.PIDFile(ServiceNginx), []byte("99999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PIDFile(ServicePHPFPM), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SocketFile("php"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	proc.procs[4242] = stubProcEntry{
		info: ProcInfo{PID: 4242, Comm: "bash", Cmdline: "/bin/bash", Alive: true},
		rss:  4096,
	}

	mgr := NewManager(paths, cfg, &fakeSys{})
	mgr.Proc = proc

	statuses, err := mgr.Status(context.Background(), []string{ServiceNginx, ServicePHPFPM})
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	for _, st := range statuses {
		if st.State != StateStopped || !st.Recovered {
			t.Errorf("expected STOPPED & Recovered=true for %s, got %+v", st.Name, st)
		}
		if _, err := os.Stat(paths.PIDFile(st.Name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan PID file for %s should have been deleted", st.Name)
		}
	}
}

func TestUpDownRestartLifecycle(t *testing.T) {
	paths := testXDGPaths(t)
	if err := os.MkdirAll(paths.NginxDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.NginxConf(), []byte("# test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.DBDir(), "mysql"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.PHPDefault = "8.3"
	proc := newFakeProcOps()

	var capturedPHPEnv []string
	sys := &fakeSys{
		bins: map[string]string{
			"nginx":      "/usr/bin/nginx",
			"php-fpm8.3": "/usr/bin/php-fpm8.3",
			"php-cgi":    `C:\php\php-cgi.exe`,
			"mariadbd":   "/usr/bin/mariadbd",
		},
	}
	sys.onRun = func(name string, args ...string) ([]byte, error) {
		switch name {
		case "/usr/bin/nginx":
			// On Linux, nginx daemonizes via Sys.Run; on Windows, reload calls `nginx -s reload`.
			if len(args) > 0 && args[len(args)-1] == "quit" {
				proc.mu.Lock()
				delete(proc.procs, 101)
				proc.mu.Unlock()
				return nil, nil
			}
			_ = os.WriteFile(paths.PIDFile(ServiceNginx), []byte("101\n"), 0o644)
			proc.mu.Lock()
			proc.procs[101] = stubProcEntry{info: ProcInfo{PID: 101, Comm: "nginx", Alive: true}, rss: 6 * 1024 * 1024}
			proc.mu.Unlock()
		case "/usr/bin/php-fpm8.3":
			_ = os.WriteFile(paths.PIDFile(ServicePHPFPM), []byte("102\n"), 0o644)
			proc.mu.Lock()
			proc.procs[102] = stubProcEntry{info: ProcInfo{PID: 102, Comm: "php-fpm", Alive: true}, rss: 16 * 1024 * 1024}
			proc.mu.Unlock()
		}
		return nil, nil
	}

	mgr := NewManager(paths, cfg, sys)
	mgr.Proc = proc
	mgr.PollInterval = time.Millisecond
	mgr.StopTimeout = 15 * time.Millisecond
	mgr.ReadyTimeout = 200 * time.Millisecond
	mgr.Dial = func(_, _ string, _ time.Duration) error { return nil }
	mgr.StartBackground = func(bin string, _ []string, _ string, extraEnv []string) (int, error) {
		switch bin {
		case "/usr/bin/nginx":
			_ = os.WriteFile(paths.PIDFile(ServiceNginx), []byte("101\n"), 0o644)
			proc.mu.Lock()
			proc.procs[101] = stubProcEntry{info: ProcInfo{PID: 101, Comm: "nginx.exe", Alive: true}, rss: 6 * 1024 * 1024}
			proc.mu.Unlock()
			return 101, nil
		case `C:\php\php-cgi.exe`:
			capturedPHPEnv = extraEnv
			proc.mu.Lock()
			proc.procs[102] = stubProcEntry{info: ProcInfo{PID: 102, Comm: "php-cgi.exe", Alive: true}, rss: 16 * 1024 * 1024}
			proc.mu.Unlock()
			return 102, nil
		default:
			_ = os.WriteFile(paths.PIDFile(ServiceMariaDB), []byte("103\n"), 0o644)
			proc.mu.Lock()
			proc.procs[103] = stubProcEntry{info: ProcInfo{PID: 103, Comm: "mariadbd", Alive: true}, rss: 64 * 1024 * 1024}
			proc.mu.Unlock()
			return 103, nil
		}
	}

	upRes, err := mgr.Up(context.Background(), cfg.Services)
	if err != nil {
		t.Fatalf("Up failed: %v", err)
	}
	if len(upRes) != 3 {
		t.Fatalf("expected 3 results, got %d", len(upRes))
	}
	if runtime.GOOS == "windows" {
		if len(capturedPHPEnv) != 2 || capturedPHPEnv[0] != "PHP_FCGI_CHILDREN=4" {
			t.Errorf("expected PHP_FCGI_CHILDREN=4 on Windows, got %v", capturedPHPEnv)
		}
	}

	// Idempotent Up
	upAgain, err := mgr.Up(context.Background(), cfg.Services)
	if err != nil {
		t.Fatalf("second Up failed: %v", err)
	}
	for _, r := range upAgain {
		if r.Changed {
			t.Errorf("expected idempotent Up for %s (Changed=false)", r.Name)
		}
	}

	// Reload behavior per OS
	proc.signals = nil
	sys.calls = nil
	if _, err := mgr.Restart(context.Background(), []string{ServiceNginx, ServicePHPFPM}, true); err != nil {
		t.Fatalf("Restart --reload failed: %v", err)
	}
	if runtime.GOOS == "linux" {
		if len(proc.signals) != 2 || proc.signals[0] != SigHup || proc.signals[1] != SigUsr2 {
			t.Errorf("expected [SIGHUP SIGUSR2] on Linux, got %v", proc.signals)
		}
	} else if runtime.GOOS == "windows" {
		foundReload := false
		for _, c := range sys.calls {
			if len(c) > 0 && c[len(c)-1] == "reload" {
				foundReload = true
			}
		}
		if !foundReload {
			t.Errorf("expected nginx -s reload call on Windows, got %+v", sys.calls)
		}
	}

	// Reverse-order Down + SIGKILL/Terminate escalation
	proc.mu.Lock()
	proc.forceSurvive[101] = true
	proc.signals = nil
	proc.mu.Unlock()

	downRes, err := mgr.Down(context.Background(), cfg.Services)
	if err != nil {
		t.Fatalf("Down failed: %v", err)
	}
	if len(downRes) != 3 || downRes[0].Name != ServiceMariaDB || downRes[2].Name != ServiceNginx {
		t.Errorf("unexpected reverse shutdown order: %+v", downRes)
	}
}

func TestRealSubprocessLifecycleAndOrphanRecovery(t *testing.T) {
	paths := testXDGPaths(t)
	cfg := config.Default()
	mgr := NewManager(paths, cfg, &fakeSys{})
	mgr.PollInterval = 10 * time.Millisecond
	mgr.StopTimeout = 500 * time.Millisecond
	mgr.MatchProcess = func(_ string, info ProcInfo) bool {
		return info.Alive
	}

	// 1) Real dead OS process -> orphan PID self-healing using real OS ProcOps.
	deadCmd := exec.Command(os.Args[0], "-test.run=^TestHelperDaemonProcess$")
	deadCmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_EXIT_NOW=1")
	if err := deadCmd.Run(); err != nil {
		t.Fatalf("dead helper run failed: %v", err)
	}
	if err := os.WriteFile(paths.PIDFile(ServiceNginx), []byte(strconv.Itoa(deadCmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	statuses, err := mgr.Status(context.Background(), []string{ServiceNginx})
	if err != nil {
		t.Fatalf("Status(orphan) failed: %v", err)
	}
	if statuses[0].State != StateStopped || !statuses[0].Recovered {
		t.Fatalf("expected real dead PID to self-heal, got %+v", statuses[0])
	}
	if _, err := os.Stat(paths.PIDFile(ServiceNginx)); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected orphan PID file to be deleted")
	}

	// 2) Real background subprocess -> InspectPID, TreeRSS > 0, and Down termination.
	tmpDir := t.TempDir()
	pidOut := filepath.Join(tmpDir, "helper.pid")
	readyOut := filepath.Join(tmpDir, "helper.ready")
	logPath := paths.LogFile(ServicePHPFPM)

	// StartBackground using test executable directly with extraEnv.
	liveCmd := exec.Command(os.Args[0], "-test.run=^TestHelperDaemonProcess$")
	liveCmd.SysProcAttr = detachSysProcAttr()
	liveCmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_PID_FILE="+pidOut,
		"HELPER_READY_FILE="+readyOut,
	)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	liveCmd.Stdout = logFile
	liveCmd.Stderr = logFile
	if err := liveCmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start live helper failed: %v", err)
	}
	_ = logFile.Close()
	livePID := liveCmd.Process.Pid
	t.Cleanup(func() {
		_ = mgr.Proc.SendSignal(livePID, SigKill)
		_ = liveCmd.Wait()
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(readyOut); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for live helper readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := os.WriteFile(paths.PIDFile(ServicePHPFPM), []byte(strconv.Itoa(livePID)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	liveStatuses, err := mgr.Status(context.Background(), []string{ServicePHPFPM})
	if err != nil {
		t.Fatalf("Status(live) failed: %v", err)
	}
	if liveStatuses[0].State != StateRunning || liveStatuses[0].PID != livePID {
		t.Fatalf("expected RUNNING pid %d, got %+v", livePID, liveStatuses[0])
	}
	if liveStatuses[0].MemoryRSS <= 0 {
		t.Errorf("expected real OS RSS memory > 0, got %d", liveStatuses[0].MemoryRSS)
	}

	downRes, err := mgr.Down(context.Background(), []string{ServicePHPFPM})
	if err != nil {
		t.Fatalf("Down(live) failed: %v", err)
	}
	if !downRes[0].Changed {
		t.Errorf("expected Down to stop live subprocess, got %+v", downRes[0])
	}
	_ = liveCmd.Wait()

	if _, alive := mgr.Proc.InspectPID(livePID); alive {
		t.Errorf("expected live helper PID %d to be terminated", livePID)
	}
}
