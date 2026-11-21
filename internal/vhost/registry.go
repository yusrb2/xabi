package vhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yusrb2/xabi/internal/sysutil"
)

// Registry holds all registered local sites stored in ~/.config/xabi/sites.toml.
type Registry struct {
	Sites []Site
}

// LoadRegistry loads sites.toml, returning an empty Registry if the file does not exist yet.
func LoadRegistry(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Registry{}, nil
		}
		return Registry{}, err
	}
	return ParseRegistry(data)
}

// SaveRegistry sorts and atomically writes the site registry to path.
func SaveRegistry(path string, reg Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sort.Slice(reg.Sites, func(i, j int) bool {
		return reg.Sites[i].Domain < reg.Sites[j].Domain
	})
	return sysutil.WriteFileAtomic(path, []byte(MarshalRegistry(reg)), 0o644)
}

// Upsert inserts or replaces a site keyed by Domain.
func (r *Registry) Upsert(site Site) {
	for i := range r.Sites {
		if strings.EqualFold(r.Sites[i].Domain, site.Domain) {
			r.Sites[i] = site
			return
		}
	}
	r.Sites = append(r.Sites, site)
}

// Remove deletes the site matching domain and reports whether it existed.
func (r *Registry) Remove(domain string) (Site, bool) {
	for i := range r.Sites {
		if strings.EqualFold(r.Sites[i].Domain, domain) {
			removed := r.Sites[i]
			r.Sites = append(r.Sites[:i], r.Sites[i+1:]...)
			return removed, true
		}
	}
	return Site{}, false
}

// FindByDomain finds a site by exact domain (case-insensitive).
func (r Registry) FindByDomain(domain string) (Site, bool) {
	for _, s := range r.Sites {
		if strings.EqualFold(s.Domain, domain) {
			return s, true
		}
	}
	return Site{}, false
}

// FindByDir matches cwd against registered site roots (including Laravel parent dir when Root ends with /public).
func (r Registry) FindByDir(cwd string) (Site, bool) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		absCwd = filepath.Clean(cwd)
	}
	cleanCwd := filepath.Clean(absCwd)
	publicCwd := filepath.Join(cleanCwd, "public")

	for _, s := range r.Sites {
		cleanRoot := filepath.Clean(s.Root)
		if samePath(cleanRoot, cleanCwd) || samePath(cleanRoot, publicCwd) {
			return s, true
		}
	}
	return Site{}, false
}

// MarshalRegistry serializes Registry to human-readable TOML.
func MarshalRegistry(reg Registry) string {
	var b strings.Builder
	b.WriteString("# Managed by xabi — registered local sites\n")
	for _, s := range reg.Sites {
		b.WriteString("\n[[site]]\n")
		fmt.Fprintf(&b, "name = %q\n", s.Name)
		fmt.Fprintf(&b, "domain = %q\n", s.Domain)
		fmt.Fprintf(&b, "kind = %q\n", s.Kind)
		fmt.Fprintf(&b, "root = %q\n", filepath.ToSlash(s.Root))
		fmt.Fprintf(&b, "proxy = %q\n", s.Proxy)
		fmt.Fprintf(&b, "php = %q\n", s.PHP)
		fmt.Fprintf(&b, "tls = %t\n", s.TLS)
	}
	return b.String()
}

// ParseRegistry parses sites.toml produced by MarshalRegistry or hand-edited by the user.
func ParseRegistry(data []byte) (Registry, error) {
	var reg Registry
	var cur *Site

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, rawLine := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(stripComment(rawLine))
		if line == "" {
			continue
		}
		if line == "[[site]]" {
			if cur != nil {
				reg.Sites = append(reg.Sites, *cur)
			}
			cur = &Site{}
			continue
		}
		if cur == nil {
			return Registry{}, fmt.Errorf("sites.toml:%d: key outside [[site]] table", lineNo)
		}

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return Registry{}, fmt.Errorf("sites.toml:%d: expected key = value", lineNo)
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])

		switch key {
		case "tls":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return Registry{}, fmt.Errorf("sites.toml:%d: invalid tls bool %q", lineNo, val)
			}
			cur.TLS = b
		case "name", "domain", "kind", "root", "proxy", "php":
			unquoted, err := strconv.Unquote(val)
			if err != nil {
				return Registry{}, fmt.Errorf("sites.toml:%d: %s: %w", lineNo, key, err)
			}
			switch key {
			case "name":
				cur.Name = unquoted
			case "domain":
				cur.Domain = unquoted
			case "kind":
				cur.Kind = Kind(unquoted)
			case "root":
				cur.Root = filepath.FromSlash(unquoted)
			case "proxy":
				cur.Proxy = unquoted
			case "php":
				cur.PHP = unquoted
			}
		}
	}
	if cur != nil {
		reg.Sites = append(reg.Sites, *cur)
	}
	return reg, nil
}

func stripComment(line string) string {
	var inQuotes, escaped bool
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuotes {
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inQuotes = false
			}
			continue
		}
		if ch == '"' {
			inQuotes = true
		} else if ch == '#' {
			return line[:i]
		}
	}
	return line
}

func samePath(a, b string) bool {
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
