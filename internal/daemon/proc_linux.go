//go:build linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type linuxProcOps struct{}

func defaultProcOps() procOps {
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

	// Build parent -> child map from /proc/<pid>/stat to sum master + workers accurately.
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
