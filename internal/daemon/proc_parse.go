package daemon

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ProcStat holds key fields extracted from /proc/<pid>/stat.
type ProcStat struct {
	PID   int
	Comm  string
	State byte
	PPID  int
}

// ParseProcStat parses a Linux /proc/<pid>/stat line safely even when comm contains spaces or parentheses.
func ParseProcStat(data []byte) (ProcStat, error) {
	line := strings.TrimSpace(string(data))
	openIdx := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if openIdx <= 0 || closeIdx <= openIdx || closeIdx+2 >= len(line) {
		return ProcStat{}, errors.New("malformed /proc stat line")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(line[:openIdx]))
	if err != nil {
		return ProcStat{}, fmt.Errorf("invalid pid in stat: %w", err)
	}

	comm := line[openIdx+1 : closeIdx]
	rest := strings.Fields(line[closeIdx+2:])
	if len(rest) < 2 || len(rest[0]) == 0 {
		return ProcStat{}, errors.New("truncated /proc stat fields")
	}

	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return ProcStat{}, fmt.Errorf("invalid ppid in stat: %w", err)
	}

	return ProcStat{
		PID:   pid,
		Comm:  comm,
		State: rest[0][0],
		PPID:  ppid,
	}, nil
}

// ParseProcStatusRSS parses VmRSS (in bytes) from Linux /proc/<pid>/status content.
func ParseProcStatusRSS(data []byte) (int64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "VmRSS:"))
		if len(fields) == 0 {
			return 0, errors.New("empty VmRSS line")
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse VmRSS: %w", err)
		}
		return kb * 1024, nil
	}
	return 0, nil
}

// MatchServiceProcess checks whether a running process's comm or cmdline belongs to the managed service.
// This prevents acting on stale PIDs reused by unrelated processes.
func MatchServiceProcess(service, comm, cmdline string) bool {
	c := strings.ToLower(comm)
	cmd := strings.ToLower(cmdline)

	switch service {
	case ServiceNginx:
		return strings.Contains(c, "nginx") || strings.Contains(cmd, "nginx")
	case ServicePHPFPM:
		return strings.Contains(c, "php-fpm") || strings.Contains(cmd, "php-fpm") ||
			(strings.Contains(c, "php") && strings.Contains(cmd, "fpm"))
	case ServiceMariaDB:
		return strings.Contains(c, "mariadbd") || strings.Contains(c, "mysqld") ||
			strings.Contains(cmd, "mariadbd") || strings.Contains(cmd, "mysqld")
	default:
		return false
	}
}

// FormatBytes formats a byte count into concise IEC units (KiB / MiB / GiB).
func FormatBytes(b int64) string {
	if b <= 0 {
		return "-"
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%d KiB", b/kib)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
