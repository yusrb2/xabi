//go:build !linux && !windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

type otherProcOps struct{}

func defaultProcOps() ProcOps {
	return otherProcOps{}
}

func (otherProcOps) InspectPID(pid int) (ProcInfo, bool) {
	if pid <= 0 {
		return ProcInfo{}, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil || proc == nil {
		return ProcInfo{}, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return ProcInfo{}, false
	}
	return ProcInfo{PID: pid, Alive: true}, true
}

func (otherProcOps) TreeRSS(_ int) int64 {
	return 0
}

func (otherProcOps) SendSignal(pid int, sig Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	switch sig {
	case SigTerm:
		return proc.Signal(syscall.SIGTERM)
	case SigKill:
		return proc.Kill()
	case SigHup:
		return proc.Signal(syscall.SIGHUP)
	case SigUsr2:
		return proc.Signal(syscall.SIGUSR2)
	default:
		return fmt.Errorf("unsupported signal %v", sig)
	}
}

func (otherProcOps) ReapIfExited(_ int) bool {
	return false
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return nil
}

func (m *LocalManager) startNginxPlatform(_ context.Context) (ActionResult, error) {
	return ActionResult{}, errors.New("unsupported platform")
}

func (m *LocalManager) startPHPFPMPlatform(_ context.Context) (ActionResult, error) {
	return ActionResult{}, errors.New("unsupported platform")
}

func (m *LocalManager) startMariaDBPlatform(_ context.Context) (ActionResult, error) {
	return ActionResult{}, errors.New("unsupported platform")
}

func (m *LocalManager) stopServicePlatform(ctx context.Context, st ServiceStatus) error {
	if err := m.Proc.SendSignal(st.PID, SigTerm); err != nil {
		return err
	}
	if !m.waitUntilExited(ctx, st.PID, m.StopTimeout) {
		_ = m.Proc.SendSignal(st.PID, SigKill)
		_ = m.waitUntilExited(ctx, st.PID, 500*time.Millisecond)
	}
	return nil
}

func (m *LocalManager) reloadServicePlatform(_ context.Context, st ServiceStatus) (ActionResult, error) {
	sig := SigHup
	if st.Name == ServicePHPFPM {
		sig = SigUsr2
	}
	if err := m.Proc.SendSignal(st.PID, sig); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Name: st.Name, PID: st.PID, Changed: true, Note: "reloaded (" + sig.String() + ")"}, nil
}

func (m *LocalManager) serviceEndpointPlatform(_ context.Context, svc string) string {
	switch svc {
	case ServiceNginx:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.HTTP)
	case ServicePHPFPM:
		return "unix:" + m.Paths.SocketFile("php")
	case ServiceMariaDB:
		return fmt.Sprintf("127.0.0.1:%d", m.Cfg.Ports.DB)
	default:
		return "-"
	}
}

func (m *LocalManager) cleanupServiceSocketsPlatform(_ context.Context, _ string) {}
