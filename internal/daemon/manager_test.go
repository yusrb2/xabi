package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

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
func (f *fakeSys) IsRoot() bool                                    { return false }
func (f *fakeSys) Run(_ context.Context, name string, args ...string) ([]byte, error) {
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

func TestParseProcStatAndStatusRSS(t *testing.T) {
	statLine := []byte("12401 (nginx: worker process) S 12400 12400 12400 0 -1 4194304 100 0 0 0 1 0 0 0 20 0 1 0 99 1024 500")
	st, err := ParseProcStat(statLine)
	if err != nil {
		t.Fatalf("ParseProcStat failed: %v", err)
	}
	if st.PID != 12401 || st.Comm != "nginx: worker process" || st.State != 'S' || st.PPID != 12400 {
		t.Errorf("unexpected ProcStat: %+v", st)
	}

	// Zombie should be parsed with state 'Z'
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
}

func TestOrphanPIDSelfHealingAndPIDReuseSafety(t *testing.T) {
	paths := testXDGPaths(t)
	cfg := config.Default()
	proc := newFakeProcOps()

	// Case 1: Dead PID in nginx.pid -> recovered and deleted.
	if err := os.WriteFile(paths.PIDFile(ServiceNginx), []byte("99999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Case 2: Reused PID belonging to unrelated process ("bash") -> recovered, NOT reported running.
	if err := os.WriteFile(paths.PIDFile(ServicePHPFPM), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PHPFPMSocketPath(paths, "system"), []byte{}, 0o644); err != nil {
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
	if _, err := os.Stat(PHPFPMSocketPath(paths, "system")); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale php-fpm socket should have been removed during orphan recovery")
	}
}

func TestUpDownRestartAndEscalationLifecycle(t *testing.T) {
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

	sys := &fakeSys{
		bins: map[string]string{
			"nginx":      "/usr/bin/nginx",
			"php-fpm8.3": "/usr/bin/php-fpm8.3",
			"mariadbd":   "/usr/bin/mariadbd",
		},
	}
	sys.onRun = func(name string, _ ...string) ([]byte, error) {
		switch name {
		case "/usr/bin/nginx":
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
	mgr.StartBackground = func(_ string, _ []string, _ string) (int, error) {
		_ = os.WriteFile(paths.PIDFile(ServiceMariaDB), []byte("103\n"), 0o644)
		proc.mu.Lock()
		proc.procs[103] = stubProcEntry{info: ProcInfo{PID: 103, Comm: "mariadbd", Alive: true}, rss: 64 * 1024 * 1024}
		proc.mu.Unlock()
		return 103, nil
	}

	// Up
	upRes, err := mgr.Up(context.Background(), cfg.Services)
	if err != nil {
		t.Fatalf("Up failed: %v", err)
	}
	if len(upRes) != 3 {
		t.Fatalf("expected 3 results, got %d", len(upRes))
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

	// Graceful reload signals: nginx=SIGHUP, php-fpm=SIGUSR2
	proc.signals = nil
	if _, err := mgr.Restart(context.Background(), []string{ServiceNginx, ServicePHPFPM}, true); err != nil {
		t.Fatalf("Restart --reload failed: %v", err)
	}
	if len(proc.signals) != 2 || proc.signals[0] != SigHup || proc.signals[1] != SigUsr2 {
		t.Errorf("expected [SIGHUP SIGUSR2], got %v", proc.signals)
	}

	// Force SIGKILL escalation when nginx ignores SIGTERM
	proc.mu.Lock()
	proc.forceSurvive[101] = true
	proc.signals = nil
	proc.mu.Unlock()

	downRes, err := mgr.Down(context.Background(), cfg.Services)
	if err != nil {
		t.Fatalf("Down failed: %v", err)
	}
	// Reverse order: mariadb -> php-fpm -> nginx
	if len(downRes) != 3 || downRes[0].Name != ServiceMariaDB || downRes[2].Name != ServiceNginx {
		t.Errorf("unexpected reverse shutdown order: %+v", downRes)
	}
	hasKill := false
	for _, s := range proc.signals {
		if s == SigKill {
			hasKill = true
		}
	}
	if !hasKill {
		t.Errorf("expected SIGKILL escalation for unresponsive process, got signals=%v", proc.signals)
	}
}
