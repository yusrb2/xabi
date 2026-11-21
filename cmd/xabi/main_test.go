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
	if !strings.Contains(out.String(), "Commands:") {
		t.Errorf("unexpected help output: %q", out.String())
	}
}
