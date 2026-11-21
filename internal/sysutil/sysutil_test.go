package sysutil

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckPortFreeAndInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	st := CheckPort(port, 50*time.Millisecond)
	if !st.InUse {
		t.Errorf("expected port %d to be reported InUse", port)
	}

	_ = ln.Close()
	st = CheckPort(port, 50*time.Millisecond)
	if st.InUse {
		t.Errorf("expected port %d to be reported free after Close (%s)", port, st.Detail)
	}
}

func TestWriteFileAtomicAndDirWritable(t *testing.T) {
	dir := t.TempDir()
	if !DirWritable(dir) {
		t.Fatalf("DirWritable(%q) = false, want true", dir)
	}

	target := filepath.Join(dir, "sample.conf")
	if err := WriteFileAtomic(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic failed: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", string(got), "hello")
	}
}

func TestCheckHostsFile(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	st := checkHostsFile(hosts)
	if !st.Exists || !st.Readable || !st.Writable {
		t.Errorf("unexpected HostsStatus: %+v", st)
	}

	missing := checkHostsFile(filepath.Join(dir, "nonexistent"))
	if missing.Exists || missing.Readable || missing.Writable {
		t.Errorf("expected missing file status false, got %+v", missing)
	}
}
