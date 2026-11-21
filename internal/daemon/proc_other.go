//go:build !linux

package daemon

import (
	"errors"
	"os"
	"syscall"
)

type otherProcOps struct{}

func defaultProcOps() procOps {
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
	case SigKill, SigTerm:
		return proc.Kill()
	default:
		return errors.New("reload signals are only supported on Linux")
	}
}

func (otherProcOps) ReapIfExited(_ int) bool {
	return false
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return nil
}
