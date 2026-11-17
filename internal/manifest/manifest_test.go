package manifest

import (
	"strings"
	"testing"
)

func TestParseLineLegacy(t *testing.T) {
	tests := []struct {
		line string
		want Entry
	}{
		{"dir usr/bin", Entry{Kind: "dir", Path: "usr/bin"}},
		{"file abc123 usr/bin/tool", Entry{Kind: "file", Hash: "abc123", Path: "usr/bin/tool"}},
		{"link /etc/init.d/tool etc/runlevels/default/tool",
			Entry{Kind: "link", Link: "/etc/init.d/tool", Path: "etc/runlevels/default/tool"}},
	}
	for _, tt := range tests {
		got, err := ParseLine(tt.line)
		if err != nil {
			t.Fatalf("ParseLine(%q): %v", tt.line, err)
		}
		if got != tt.want {
			t.Errorf("ParseLine(%q) = %+v, want %+v", tt.line, got, tt.want)
		}
	}
}

func TestPathsWithSpaces(t *testing.T) {
	entries := []Entry{
		{Kind: "file", Hash: "d24a", Path: "lib/firmware/brcm/brcmfmac43241b4-sdio.Intel Corp.-VALLEYVIEW C0 PLATFORM.txt"},
		{Kind: "dir", Path: "lib/firmware/with space"},
		{Kind: "link", Link: "/usr/lib/firmware/target with space.bin", Path: "lib/firmware/link name.bin"},
		{Kind: "file", Hash: "beef", Path: `lib/firmware/quote"and\backslash.bin`},
	}
	for _, e := range entries {
		line := e.String()
		got, err := ParseLine(line)
		if err != nil {
			t.Fatalf("ParseLine(%q): %v", line, err)
		}
		if got != e {
			t.Errorf("round trip of %+v via %q = %+v", e, line, got)
		}
	}
}

func TestParseLineUnquotedLegacySpacePath(t *testing.T) {
	// Manifests written before quoting existed contain raw spaces. They are
	// read back verbatim (the old parser rejected them, which is why this
	// compatibility matters for already-installed packages).
	line := "file d24a lib/firmware/brcm/brcmfmac43241b4-sdio.Intel Corp.-VALLEYVIEW C0 PLATFORM.txt"
	got, err := ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Path, "C0 PLATFORM.txt") || got.Hash != "d24a" {
		t.Errorf("parsed %+v", got)
	}
}

func TestParseLineErrors(t *testing.T) {
	for _, line := range []string{"", "bogus x", "file onlyhash", `file abc "unterminated`} {
		if _, err := ParseLine(line); err == nil {
			t.Errorf("ParseLine(%q) should fail", line)
		}
	}
}
