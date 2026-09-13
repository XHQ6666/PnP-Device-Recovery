package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeInstanceID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"pci\\ven_10de&dev_2520\\1", "PCI\\VEN_10DE&DEV_2520\\1"},
		{"  PCI\\VEN_10DE\\1  ", "PCI\\VEN_10DE\\1"},
		{"", ""},
		{"Display\\Foo", "DISPLAY\\FOO"},
	}
	for _, c := range cases {
		if got := NormalizeInstanceID(c.in); got != c.want {
			t.Fatalf("NormalizeInstanceID(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestEqualFoldInstanceID(t *testing.T) {
	if !EqualFoldInstanceID("PCI\\VEN_10DE\\1", "pci\\ven_10de\\1") {
		t.Fatal("should fold")
	}
	if EqualFoldInstanceID("PCI\\VEN_10DE\\1", "PCI\\VEN_10DE\\2") {
		t.Fatal("different IDs")
	}
	if !EqualFoldInstanceID("  abc  ", "ABC") {
		t.Fatal("trim+fold")
	}
}

func TestSessionKeyFold(t *testing.T) {
	a := sessionKey("pci\\ven_10de\\1", "GPU", 0)
	b := sessionKey("PCI\\VEN_10DE\\1", "GPU", 0)
	if a != b {
		t.Fatalf("keys differ: %q vs %q", a, b)
	}
	c := sessionKey("", "GPU", 0)
	if c != "cfg:0:GPU" {
		t.Fatalf("empty instance key: %q", c)
	}
}


func TestResolvePathRelativeToExe(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "abs.log")
	got, err := ResolvePathRelativeToExe(abs)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("abs: got %q want %q", got, abs)
	}
	empty, err := ResolvePathRelativeToExe("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(empty) != defaultLogFilename {
		t.Fatalf("empty default: %q", empty)
	}
	rel, err := ResolvePathRelativeToExe("rel.log")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(rel) != "rel.log" || !filepath.IsAbs(rel) {
		t.Fatalf("rel: %q", rel)
	}
}
