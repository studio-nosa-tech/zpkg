package spdx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/provenance"
)

func sampleInfo(name, version string) *provenance.Info {
	return &provenance.Info{
		Schema:  provenance.Schema,
		Package: provenance.Package{Name: name, Version: version, Slot: "0", Arch: "amd64"},
		Recipe:  provenance.Recipe{Digest: "recipe-digest"},
		Sources: []provenance.Source{{URL: "https://example.invalid/" + name + ".tar.xz", SHA256: "s"}},
		Build: provenance.Build{
			SourceDateEpoch: provenance.DefaultSourceDateEpoch,
			Sandbox:         "bwrap",
			Network:         "on",
			ZpkgVersion:     "0.2.0",
		},
		Dependencies: []provenance.Component{{Name: "glibc", Version: "2.44"}},
		BuildDeps:    []provenance.Component{{Name: "gcc", Version: "16.2.0"}},
		Toolchain: []provenance.Component{
			{Name: "gcc", Version: "16.2.0"},
			{Name: "glibc", Version: "2.44"},
		},
		Environment: provenance.Environment{Hash: "envhash", Count: 2, Packages: []string{"gcc-16.2.0", "glibc-2.44"}},
		Artifact: provenance.Artifact{
			Filename: name + "-" + version + ".pkg.tar.gz",
			SHA256:   strings.Repeat("a", 64),
		},
	}
}

func TestPackageDocument(t *testing.T) {
	doc := PackageDocument(sampleInfo("hello", "1.0"))
	if doc.SPDXVersion != Version || doc.DataLicense != "CC0-1.0" || doc.SPDXID != DocumentID {
		t.Fatalf("document header = %+v", doc)
	}
	if !strings.HasPrefix(doc.DocumentNamespace, "https://") {
		t.Fatalf("namespace = %q", doc.DocumentNamespace)
	}
	if len(doc.Packages) < 3 {
		t.Fatalf("packages = %d", len(doc.Packages))
	}
	ids := map[string]bool{}
	for _, p := range doc.Packages {
		if ids[p.SPDXID] {
			t.Fatalf("duplicate SPDXID %s", p.SPDXID)
		}
		ids[p.SPDXID] = true
		if p.DownloadLocation == "" || p.LicenseConcluded != "NOASSERTION" || p.FilesAnalyzed {
			t.Fatalf("invalid package %+v", p)
		}
	}
	target := PackageID("hello", "1.0")
	dep := PackageID("glibc", "2.44")
	buildDep := PackageID("gcc", "16.2.0")
	foundRuntime, foundBuild, foundTool := false, false, false
	for _, r := range doc.Relationships {
		if !ids[r.SPDXElementID] && r.SPDXElementID != DocumentID {
			t.Fatalf("relationship source %s not a package", r.SPDXElementID)
		}
		if !ids[r.RelatedSPDXElement] {
			t.Fatalf("relationship target %s not a package", r.RelatedSPDXElement)
		}
		if r.SPDXElementID == target && r.RelatedSPDXElement == dep && r.RelationshipType == "DEPENDS_ON" {
			foundRuntime = true
		}
		if r.SPDXElementID == buildDep && r.RelatedSPDXElement == target && r.RelationshipType == "BUILD_DEPENDENCY_OF" {
			foundBuild = true
		}
		if r.SPDXElementID == PackageID("gcc", "16.2.0") && r.RelatedSPDXElement == target && r.RelationshipType == "BUILD_TOOL_OF" {
			foundTool = true
		}
	}
	if !foundRuntime || !foundBuild || !foundTool {
		t.Fatalf("relationships missing: runtime=%v build=%v tool=%v\n%+v", foundRuntime, foundBuild, foundTool, doc.Relationships)
	}
	if _, err := json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
}

func TestSystemDocument(t *testing.T) {
	doc := SystemDocument([]*provenance.Info{
		sampleInfo("hello", "1.0"),
		sampleInfo("world", "2.0"),
	}, "0.2.0")
	if len(doc.Packages) != 3 {
		t.Fatalf("packages = %d", len(doc.Packages))
	}
	foundSystem, foundHello, foundWorld := false, false, false
	for _, p := range doc.Packages {
		switch p.SPDXID {
		case SystemID:
			foundSystem = true
		case PackageID("hello", "1.0"):
			foundHello = true
		case PackageID("world", "2.0"):
			foundWorld = true
		}
	}
	if !foundSystem || !foundHello || !foundWorld {
		t.Fatalf("system document packages = %+v", doc.Packages)
	}
	contains := 0
	for _, r := range doc.Relationships {
		if r.SPDXElementID == SystemID && r.RelationshipType == "CONTAINS" {
			contains++
		}
	}
	if contains != 2 {
		t.Fatalf("CONTAINS relationships = %d", contains)
	}
}

func TestPackageIDStableAndUnique(t *testing.T) {
	a := PackageID("hello", "1.0")
	if a != PackageID("hello", "1.0") {
		t.Fatal("PackageID is not stable")
	}
	if a == PackageID("hello", "1.1") || a == PackageID("world", "1.0") {
		t.Fatal("PackageID collision")
	}
	if !strings.HasPrefix(a, "SPDXRef-") {
		t.Fatalf("PackageID = %q", a)
	}
}
