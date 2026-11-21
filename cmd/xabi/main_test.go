package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/cli"
)

func TestRunVersionAndHelp(t *testing.T) {
	var out, errBuf bytes.Buffer
	s := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	if code := run([]string{"--version"}, s); code != cli.ExitOK {
		t.Fatalf("version exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "xabi ") {
		t.Errorf("unexpected version output: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := run([]string{"--help"}, s); code != cli.ExitOK {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	for _, want := range []string{"Commands:", "init", "doctor", "up", "down", "restart", "status (st)", "add", "rm", "ls", "open", "tls"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output missing %q: %q", want, out.String())
		}
	}

	out.Reset()
	errBuf.Reset()
	if code := run([]string{"help", "st"}, s); code != cli.ExitOK {
		t.Fatalf("help st exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "xabi status") {
		t.Errorf("help st missing status usage: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := run([]string{"init", "--help"}, s); code != cli.ExitOK {
		t.Fatalf("init --help exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "--dry-run") {
		t.Errorf("init help missing --dry-run: %q", out.String())
	}
}
