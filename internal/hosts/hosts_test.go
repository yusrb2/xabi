package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostsAddRemovePreservesManualLinesAndEOL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	initial := "# Manual header\r\n127.0.0.1 localhost\r\n::1 localhost\r\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(path)

	added, err := mgr.Add("app.test")
	if err != nil || !added {
		t.Fatalf("Add(app.test) = (%v, %v), want (true, nil)", added, err)
	}
	// Idempotent second add
	addedAgain, err := mgr.Add("app.test")
	if err != nil || addedAgain {
		t.Fatalf("second Add(app.test) = (%v, %v), want (false, nil)", addedAgain, err)
	}
	if _, err := mgr.Add("api.test"); err != nil {
		t.Fatalf("Add(api.test) failed: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "\r\n") {
		t.Error("expected CRLF line endings to be preserved")
	}
	for _, want := range []string{
		"# Manual header",
		"127.0.0.1 localhost",
		"# BEGIN xabi",
		"127.0.0.1 app.test",
		"127.0.0.1 api.test",
		"# END xabi",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("hosts missing %q:\n%s", want, text)
		}
	}

	list, err := mgr.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List() = (%v, %v), want 2 entries", list, err)
	}

	// Remove one entry, second remains
	removed, err := mgr.Remove("app.test")
	if err != nil || !removed {
		t.Fatalf("Remove(app.test) = (%v, %v)", removed, err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "app.test") || !strings.Contains(string(raw), "api.test") {
		t.Errorf("unexpected hosts after removing app.test:\n%s", string(raw))
	}

	// Remove last entry -> managed block completely removed, manual lines intact
	if _, err := mgr.Remove("api.test"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "# BEGIN xabi") || strings.Contains(string(raw), "# END xabi") {
		t.Errorf("expected empty managed block to be removed:\n%s", string(raw))
	}
	if !strings.Contains(string(raw), "127.0.0.1 localhost") {
		t.Errorf("manual entry was lost:\n%s", string(raw))
	}
}

func TestHostsMalformedBlockReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n# BEGIN xabi\n127.0.0.1 app.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(path)
	if _, err := mgr.Add("new.test"); err == nil {
		t.Fatal("expected error when # END xabi is missing")
	}
}
