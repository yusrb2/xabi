// Package config implements a zero-dependency TOML parser/writer and default templates for xabi.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yusrb2/xabi/internal/sysutil"
)

// Ports holds the listening ports configured for xabi services.
type Ports struct {
	HTTP  int
	HTTPS int
	DB    int
}

// Config represents the user-editable ~/.config/xabi/config.toml settings.
type Config struct {
	TLD        string
	WebServer  string
	PHPDefault string
	Services   []string
	Ports      Ports
}

// Default returns the standard xabi configuration.
func Default() Config {
	return Config{
		TLD:        "test",
		WebServer:  "nginx",
		PHPDefault: "system",
		Services:   []string{"nginx", "php-fpm", "mariadb"},
		Ports: Ports{
			HTTP:  80,
			HTTPS: 443,
			DB:    3306,
		},
	}
}

// Load reads and parses config.toml from path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

// LoadOrDefault reads config.toml if present, or returns Default() when the file does not exist.
func LoadOrDefault(path string) (Config, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), false, nil
		}
		return Config{}, false, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, true, err
	}
	return cfg, true, nil
}

// Save validates and atomically writes cfg to path.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config parent dir: %w", err)
	}
	return sysutil.WriteFileAtomic(path, []byte(cfg.Marshal()), 0o644)
}

// Validate checks that configuration values are well-formed.
func (c *Config) Validate() error {
	c.TLD = strings.TrimPrefix(strings.TrimSpace(c.TLD), ".")
	if c.TLD == "" || strings.ContainsAny(c.TLD, " \t/\\:") {
		return fmt.Errorf("invalid tld %q", c.TLD)
	}

	c.WebServer = strings.ToLower(strings.TrimSpace(c.WebServer))
	if c.WebServer != "nginx" && c.WebServer != "caddy" {
		return fmt.Errorf("unsupported web_server %q (expected nginx or caddy)", c.WebServer)
	}

	c.PHPDefault = strings.TrimSpace(c.PHPDefault)
	if c.PHPDefault == "" {
		c.PHPDefault = "system"
	}

	for name, port := range map[string]int{
		"ports.http":  c.Ports.HTTP,
		"ports.https": c.Ports.HTTPS,
		"ports.db":    c.Ports.DB,
	} {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s out of range (1-65535): %d", name, port)
		}
	}

	if c.Ports.HTTP == c.Ports.HTTPS {
		return fmt.Errorf("ports.http and ports.https cannot share port %d", c.Ports.HTTP)
	}
	if c.Ports.DB == c.Ports.HTTP || c.Ports.DB == c.Ports.HTTPS {
		return fmt.Errorf("ports.db (%d) conflicts with HTTP/HTTPS port", c.Ports.DB)
	}
	return nil
}

// Marshal serializes Config into clean, human-editable TOML.
func (c Config) Marshal() string {
	var b strings.Builder
	b.WriteString("# xabi configuration — user-space local web dev orchestrator\n\n")
	fmt.Fprintf(&b, "tld = %s\n", quoteTOMLString(c.TLD))
	fmt.Fprintf(&b, "web_server = %s\n", quoteTOMLString(c.WebServer))
	fmt.Fprintf(&b, "php_default = %s\n", quoteTOMLString(c.PHPDefault))
	b.WriteString("services = [")
	for i, svc := range c.Services {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteTOMLString(svc))
	}
	b.WriteString("]\n\n")
	b.WriteString("[ports]\n")
	fmt.Fprintf(&b, "http = %d\n", c.Ports.HTTP)
	fmt.Fprintf(&b, "https = %d\n", c.Ports.HTTPS)
	fmt.Fprintf(&b, "db = %d\n", c.Ports.DB)
	return b.String()
}

// Parse parses a minimal TOML subset into Config, starting from Default() values.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	section := ""
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(stripTOMLComment(lines[i]))
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return Config{}, fmt.Errorf("config line %d: unclosed section header", lineNo)
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return Config{}, fmt.Errorf("config line %d: expected key = value", lineNo)
		}

		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		rawVal := strings.TrimSpace(line[eq+1:])

		// Accumulate multi-line array values cleanly.
		if strings.HasPrefix(rawVal, "[") && !hasBalancedBrackets(rawVal) {
			var combined strings.Builder
			combined.WriteString(rawVal)
			closed := false
			for i+1 < len(lines) {
				i++
				nextPart := strings.TrimSpace(stripTOMLComment(lines[i]))
				if nextPart == "" {
					continue
				}
				combined.WriteByte(' ')
				combined.WriteString(nextPart)
				if hasBalancedBrackets(combined.String()) {
					closed = true
					break
				}
			}
			if !closed {
				return Config{}, fmt.Errorf("config line %d: unclosed array for %q", lineNo, key)
			}
			rawVal = combined.String()
		}

		if err := applyTOMLField(&cfg, section, key, rawVal, lineNo); err != nil {
			return Config{}, err
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyTOMLField(cfg *Config, section, key, rawVal string, lineNo int) error {
	switch section {
	case "":
		switch key {
		case "tld":
			v, err := parseTOMLString(rawVal)
			if err != nil {
				return fmt.Errorf("config line %d: tld: %w", lineNo, err)
			}
			cfg.TLD = v
		case "web_server":
			v, err := parseTOMLString(rawVal)
			if err != nil {
				return fmt.Errorf("config line %d: web_server: %w", lineNo, err)
			}
			cfg.WebServer = v
		case "php_default":
			v, err := parseTOMLString(rawVal)
			if err != nil {
				return fmt.Errorf("config line %d: php_default: %w", lineNo, err)
			}
			cfg.PHPDefault = v
		case "services":
			arr, err := parseTOMLStringArray(rawVal)
			if err != nil {
				return fmt.Errorf("config line %d: services: %w", lineNo, err)
			}
			cfg.Services = arr
		}
	case "ports":
		p, err := strconv.Atoi(rawVal)
		if err != nil {
			return fmt.Errorf("config line %d: ports.%s: invalid integer %q", lineNo, key, rawVal)
		}
		switch key {
		case "http":
			cfg.Ports.HTTP = p
		case "https":
			cfg.Ports.HTTPS = p
		case "db":
			cfg.Ports.DB = p
		}
	}
	return nil
}

func stripTOMLComment(line string) string {
	var inDouble, inSingle, escaped bool
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble {
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		switch ch {
		case '"':
			inDouble = true
		case '\'':
			inSingle = true
		case '#':
			return line[:i]
		}
	}
	return line
}

func hasBalancedBrackets(s string) bool {
	var inDouble, inSingle, escaped bool
	depth := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble {
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		switch ch {
		case '"':
			inDouble = true
		case '\'':
			inSingle = true
		case '[':
			depth++
		case ']':
			depth--
		}
	}
	return depth <= 0
}

func parseTOMLString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], nil
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("expected quoted string, got %q", raw)
	}

	var b strings.Builder
	for i := 1; i < len(raw)-1; i++ {
		ch := raw[i]
		if ch != '\\' {
			b.WriteByte(ch)
			continue
		}
		i++
		if i >= len(raw)-1 {
			return "", errors.New("trailing backslash in string")
		}
		switch raw[i] {
		case '"', '\\':
			b.WriteByte(raw[i])
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			return "", fmt.Errorf("unsupported escape \\%c", raw[i])
		}
	}
	return b.String(), nil
}

func parseTOMLStringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("expected array [...], got %q", raw)
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	if inner == "" {
		return []string{}, nil
	}

	var items []string
	var cur strings.Builder
	var inDouble, inSingle, escaped bool

	flush := func() error {
		tok := strings.TrimSpace(cur.String())
		cur.Reset()
		if tok == "" {
			return nil
		}
		val, err := parseTOMLString(tok)
		if err != nil {
			return err
		}
		items = append(items, val)
		return nil
	}

	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			continue
		}
		if inDouble {
			cur.WriteByte(ch)
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			cur.WriteByte(ch)
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		switch ch {
		case '"':
			inDouble = true
			cur.WriteByte(ch)
		case '\'':
			inSingle = true
			cur.WriteByte(ch)
		case ',':
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if inDouble || inSingle {
		return nil, errors.New("unterminated string inside array")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return items, nil
}

func quoteTOMLString(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return `"` + replacer.Replace(s) + `"`
}
