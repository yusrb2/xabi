// Package hosts atomically manages xabi domain entries inside the OS hosts file.
package hosts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yusrb2/xabi/internal/sysutil"
)

const (
	beginMarker = "# BEGIN xabi"
	endMarker   = "# END xabi"
)

// Manager reads and updates the managed xabi block in a hosts file.
type Manager struct {
	Path string
}

// NewManager creates a hosts Manager targeting path (or ResolveHostsPath if empty).
func NewManager(path string) Manager {
	if path == "" {
		path = sysutil.ResolveHostsPath()
	}
	return Manager{Path: path}
}

// List returns all domains currently listed inside the managed xabi block.
func (m Manager) List() ([]string, error) {
	data, err := os.ReadFile(m.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	_, blockLines, _, _, err := splitHosts(string(data))
	if err != nil {
		return nil, err
	}
	domains := make([]string, 0, len(blockLines))
	for _, line := range blockLines {
		if d := parseManagedLine(line); d != "" {
			domains = append(domains, d)
		}
	}
	return domains, nil
}

// Add inserts "127.0.0.1 <domain>" idempotently into the managed xabi block.
func (m Manager) Add(domain string) (bool, error) {
	domain, err := normalizeHost(domain)
	if err != nil {
		return false, err
	}

	raw, eol, mode, err := m.readExisting()
	if err != nil {
		return false, err
	}

	before, blockLines, after, found, err := splitHosts(raw)
	if err != nil {
		return false, err
	}

	for _, line := range blockLines {
		if strings.EqualFold(parseManagedLine(line), domain) {
			return false, nil
		}
	}

	blockLines = append(blockLines, "127.0.0.1 "+domain)
	updated := renderHosts(before, blockLines, after, found, eol)
	if err := m.writeUpdated(updated, mode); err != nil {
		return false, err
	}
	return true, nil
}

// Remove deletes all entries matching domain from the managed xabi block.
func (m Manager) Remove(domain string) (bool, error) {
	domain, err := normalizeHost(domain)
	if err != nil {
		return false, err
	}

	raw, eol, mode, err := m.readExisting()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	before, blockLines, after, found, err := splitHosts(raw)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	kept := make([]string, 0, len(blockLines))
	removed := false
	for _, line := range blockLines {
		if strings.EqualFold(parseManagedLine(line), domain) {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return false, nil
	}

	updated := renderHosts(before, kept, after, true, eol)
	if err := m.writeUpdated(updated, mode); err != nil {
		return false, err
	}
	return true, nil
}

func (m Manager) readExisting() (raw string, eol string, mode os.FileMode, err error) {
	mode = 0o644
	eol = defaultEOL()

	info, statErr := os.Stat(m.Path)
	if statErr == nil {
		mode = info.Mode().Perm()
	}

	data, err := os.ReadFile(m.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", eol, mode, nil
		}
		return "", eol, mode, m.wrapPermError("read", err)
	}
	s := string(data)
	if strings.Contains(s, "\r\n") {
		eol = "\r\n"
	} else if strings.Contains(s, "\n") {
		eol = "\n"
	}
	return s, eol, mode, nil
}

func (m Manager) writeUpdated(content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(m.Path), 0o755); err != nil && !sysutil.IsPermissionErr(err) {
		return err
	}

	err := sysutil.WriteFileAtomic(m.Path, []byte(content), mode)
	if err == nil {
		return nil
	}

	// Fallback when /etc/hosts (or Windows hosts) is directly writable via ACL/permissions
	// while its parent directory restricts temp-file creation or rename.
	if sysutil.IsPermissionErr(err) || isSharingViolation(err) {
		if directErr := writeDirectExisting(m.Path, []byte(content)); directErr == nil {
			return nil
		} else {
			return m.wrapPermError("write", directErr)
		}
	}
	return err
}

func writeDirectExisting(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, wErr := f.Write(data)
	cErr := f.Close()
	if wErr != nil {
		return wErr
	}
	return cErr
}

func (m Manager) wrapPermError(op string, err error) error {
	if !sysutil.IsPermissionErr(err) {
		return err
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("permission denied trying to %s %s (re-run terminal as Administrator): %w", op, m.Path, err)
	}
	return fmt.Errorf("permission denied trying to %s %s (run with sudo or grant write ACL: sudo setfacl -m u:$USER:rw %s): %w", op, m.Path, m.Path, err)
}

func splitHosts(raw string) (before []string, block []string, after []string, found bool, err error) {
	if raw == "" {
		return nil, nil, nil, false, nil
	}
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(norm, "\n")

	beginIdx := -1
	endIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case beginMarker:
			if beginIdx != -1 {
				return nil, nil, nil, false, errors.New("malformed hosts file: duplicate # BEGIN xabi marker")
			}
			beginIdx = i
		case endMarker:
			if beginIdx == -1 || endIdx != -1 {
				return nil, nil, nil, false, errors.New("malformed hosts file: unexpected # END xabi marker")
			}
			endIdx = i
		}
	}

	if beginIdx != -1 && endIdx == -1 {
		return nil, nil, nil, false, errors.New("malformed hosts file: found # BEGIN xabi without matching # END xabi")
	}
	if beginIdx == -1 {
		return lines, nil, nil, false, nil
	}

	return lines[:beginIdx], lines[beginIdx+1 : endIdx], lines[endIdx+1:], true, nil
}

func renderHosts(before, block, after []string, found bool, eol string) string {
	// Trim trailing empty element produced by final newline in strings.Split.
	trimFinalEmpty := func(lines []string) []string {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			return lines[:len(lines)-1]
		}
		return lines
	}

	var out []string
	if found {
		out = append(out, before...)
		if len(block) > 0 {
			out = append(out, beginMarker)
			out = append(out, block...)
			out = append(out, endMarker)
		}
		out = append(out, trimFinalEmpty(after)...)
		if len(block) == 0 && len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
	} else {
		before = trimFinalEmpty(before)
		out = append(out, before...)
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, beginMarker)
		out = append(out, block...)
		out = append(out, endMarker)
	}

	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, eol) + eol
}

func parseManagedLine(line string) string {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		line = line[:idx]
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	return strings.ToLower(fields[1])
}

func normalizeHost(domain string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" || strings.ContainsAny(d, " \t\r\n#/\\") {
		return "", fmt.Errorf("invalid hostname %q", domain)
	}
	return d, nil
}

func defaultEOL() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

func isSharingViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "used by another process") || strings.Contains(msg, "sharing violation")
}
