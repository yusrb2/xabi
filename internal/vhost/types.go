// Package vhost manages site registration, project auto-detection, and Nginx virtual host configs.
package vhost

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Kind identifies how a virtual host serves traffic.
type Kind string

const (
	KindPHP    Kind = "php"
	KindStatic Kind = "static"
	KindProxy  Kind = "proxy"
)

// Site describes a registered local web project or reverse proxy.
type Site struct {
	Name   string
	Domain string
	Kind   Kind
	Root   string
	Proxy  string
	PHP    string
	TLS    bool
}

// NormalizeSiteName validates and normalizes a site name (stripping optional .<tld> suffix).
func NormalizeSiteName(raw, tld string) (name string, domain string, err error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimSuffix(s, ".")
	tld = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tld), "."))
	if tld == "" {
		tld = "test"
	}

	suffix := "." + tld
	s = strings.TrimSuffix(s, suffix)
	if s == "" {
		return "", "", fmt.Errorf("invalid site name %q", raw)
	}

	labels := strings.Split(s, ".")
	for _, lbl := range labels {
		if !isValidDNSLabel(lbl) {
			return "", "", fmt.Errorf("invalid site name %q (use letters, digits, hyphens)", raw)
		}
	}
	return s, s + suffix, nil
}

// SanitizeDirName converts a directory name into a valid site name label.
func SanitizeDirName(dir string) string {
	base := strings.ToLower(filepath.Base(dir))
	var b strings.Builder
	prevDash := false
	for i := 0; i < len(base); i++ {
		ch := base[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteByte(ch)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "site"
	}
	return out
}

// NormalizeProxy validates and normalizes --proxy values ("3000", ":3000", "localhost:3000", "http://127.0.0.1:3000").
func NormalizeProxy(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty proxy target")
	}

	scheme := "http"
	switch {
	case strings.HasPrefix(s, "http://"):
		s = strings.TrimPrefix(s, "http://")
	case strings.HasPrefix(s, "https://"):
		scheme = "https"
		s = strings.TrimPrefix(s, "https://")
	}
	s = strings.TrimRight(s, "/")

	if port, err := strconv.Atoi(s); err == nil {
		if port < 1 || port > 65535 {
			return "", fmt.Errorf("proxy port out of range (1-65535): %d", port)
		}
		return fmt.Sprintf("%s://127.0.0.1:%d", scheme, port), nil
	}

	if strings.HasPrefix(s, ":") {
		s = "127.0.0.1" + s
	}

	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("invalid proxy target %q: %w", raw, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid proxy port %q", portStr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, strconv.Itoa(port))), nil
}

// DetectProject inspects projectDir to determine document root and site kind.
func DetectProject(projectDir, proxyArg, phpVersion string) (kind Kind, docRoot string, normalizedProxy string, err error) {
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(absDir)
	if err != nil || !info.IsDir() {
		return "", "", "", fmt.Errorf("project directory %q does not exist", absDir)
	}

	if strings.TrimSpace(proxyArg) != "" {
		proxyURL, err := NormalizeProxy(proxyArg)
		if err != nil {
			return "", "", "", err
		}
		return KindProxy, absDir, proxyURL, nil
	}

	publicIdx := filepath.Join(absDir, "public", "index.php")
	if fileExists(publicIdx) {
		return KindPHP, filepath.Join(absDir, "public"), "", nil
	}

	if fileExists(filepath.Join(absDir, "index.php")) || strings.TrimSpace(phpVersion) != "" {
		return KindPHP, absDir, "", nil
	}

	return KindStatic, absDir, "", nil
}

func isValidDNSLabel(lbl string) bool {
	if len(lbl) == 0 || len(lbl) > 63 {
		return false
	}
	for i := 0; i < len(lbl); i++ {
		ch := lbl[i]
		isAlphaNum := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if isAlphaNum {
			continue
		}
		if ch == '-' && i > 0 && i < len(lbl)-1 {
			continue
		}
		return false
	}
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
