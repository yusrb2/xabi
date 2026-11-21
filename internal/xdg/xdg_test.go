package xdg

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWithDefaults(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	p, err := ResolveWith(func(string) string { return "" }, home)
	if err != nil {
		t.Fatalf("ResolveWith returned error: %v", err)
	}

	wantCfg := filepath.Join(home, ".config", "xabi")
	wantData := filepath.Join(home, ".local", "share", "xabi")
	if p.ConfigDir != wantCfg {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, wantCfg)
	}
	if p.DataDir != wantData {
		t.Errorf("DataDir = %q, want %q", p.DataDir, wantData)
	}
	if p.RuntimeDir == "" {
		t.Error("RuntimeDir must not be empty")
	}
}

func TestResolveWithEnvOverrides(t *testing.T) {
	base := t.TempDir()
	cfgBase := filepath.Join(base, "cfg")
	dataBase := filepath.Join(base, "data")
	rtBase := filepath.Join(base, "run")

	env := map[string]string{
		"XDG_CONFIG_HOME": cfgBase,
		"XDG_DATA_HOME":   dataBase,
		"XDG_RUNTIME_DIR": rtBase,
	}

	p, err := ResolveWith(func(k string) string { return env[k] }, filepath.Join(base, "unused-home"))
	if err != nil {
		t.Fatalf("ResolveWith returned error: %v", err)
	}

	if got, want := p.ConfigDir, filepath.Join(cfgBase, "xabi"); got != want {
		t.Errorf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := p.DataDir, filepath.Join(dataBase, "xabi"); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
	if got, want := p.RuntimeDir, filepath.Join(rtBase, "xabi"); got != want {
		t.Errorf("RuntimeDir = %q, want %q", got, want)
	}
}

func TestResolveWithIgnoresRelativeXDGPaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	env := map[string]string{
		"XDG_CONFIG_HOME": "relative/config",
		"XDG_DATA_HOME":   "./relative/data",
		"XDG_RUNTIME_DIR": "relative/run",
	}

	p, err := ResolveWith(func(k string) string { return env[k] }, home)
	if err != nil {
		t.Fatalf("ResolveWith returned error: %v", err)
	}

	if got, want := p.ConfigDir, filepath.Join(home, ".config", "xabi"); got != want {
		t.Errorf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := p.DataDir, filepath.Join(home, ".local", "share", "xabi"); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
	if !filepath.IsAbs(p.RuntimeDir) {
		t.Errorf("RuntimeDir = %q, must be absolute", p.RuntimeDir)
	}
}

func TestResolveWithMissingHomeError(t *testing.T) {
	_, err := ResolveWith(func(string) string { return "" }, "")
	if err == nil {
		t.Fatal("expected error when homeDir and XDG vars are empty")
	}
}

func TestEnsureCreatesDirectoriesAndSubpaths(t *testing.T) {
	base := t.TempDir()
	env := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(base, "cfg"),
		"XDG_DATA_HOME":   filepath.Join(base, "data"),
		"XDG_RUNTIME_DIR": filepath.Join(base, "run"),
	}

	p, err := ResolveWith(func(k string) string { return env[k] }, base)
	if err != nil {
		t.Fatalf("ResolveWith returned error: %v", err)
	}

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}

	for _, dir := range []string{p.ConfigDir, p.DataDir, p.RuntimeDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat(%q) failed: %v", dir, err)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}

	if runtime.GOOS == "linux" {
		info, err := os.Stat(p.RuntimeDir)
		if err != nil {
			t.Fatalf("Stat(%q) failed: %v", p.RuntimeDir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("RuntimeDir permissions = %o, want 0700", perm)
		}
	}

	if got, want := p.ConfigFile(), filepath.Join(p.ConfigDir, "config.toml"); got != want {
		t.Errorf("ConfigFile() = %q, want %q", got, want)
	}
	if got, want := p.DBDir(), filepath.Join(p.DataDir, "db"); got != want {
		t.Errorf("DBDir() = %q, want %q", got, want)
	}
	if got, want := p.PIDFile("nginx"), filepath.Join(p.RuntimeDir, "nginx.pid"); got != want {
		t.Errorf("PIDFile() = %q, want %q", got, want)
	}
	if got, want := p.SocketFile("php-fpm"), filepath.Join(p.RuntimeDir, "php-fpm.sock"); got != want {
		t.Errorf("SocketFile() = %q, want %q", got, want)
	}
}
