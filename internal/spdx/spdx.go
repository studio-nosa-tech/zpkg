// Package spdx generates SPDX 2.3 JSON SBOMs from zpkg provenance records.
package spdx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/provenance"
)

const (
	// Version is the SPDX specification version emitted by this package.
	Version = "SPDX-2.3"
	// DocumentID is the fixed SPDXID of the document itself.
	DocumentID = "SPDXRef-DOCUMENT"
	// SystemID is the synthetic package that contains every installed package.
	SystemID = "SPDXRef-System-zignos"
)

// Document is an SPDX 2.3 JSON document.
type Document struct {
	SPDXVersion       string         `json:"spdxVersion"`
	DataLicense       string         `json:"dataLicense"`
	SPDXID            string         `json:"SPDXID"`
	Name              string         `json:"name"`
	DocumentNamespace string         `json:"documentNamespace"`
	CreationInfo      CreationInfo   `json:"creationInfo"`
	Packages          []Package      `json:"packages"`
	Relationships     []Relationship `json:"relationships,omitempty"`
}

// CreationInfo is the SPDX creation metadata.
type CreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

// Package is one SPDX package entry.
type Package struct {
	SPDXID           string        `json:"SPDXID"`
	Name             string        `json:"name"`
	VersionInfo      string        `json:"versionInfo,omitempty"`
	PackageFileName  string        `json:"packageFileName,omitempty"`
	DownloadLocation string        `json:"downloadLocation"`
	FilesAnalyzed    bool          `json:"filesAnalyzed"`
	LicenseConcluded string        `json:"licenseConcluded"`
	LicenseDeclared  string        `json:"licenseDeclared"`
	CopyrightText    string        `json:"copyrightText"`
	Checksums        []Checksum    `json:"checksums,omitempty"`
	ExternalRefs     []ExternalRef `json:"externalRefs,omitempty"`
	SourceInfo       string        `json:"sourceInfo,omitempty"`
}

// Checksum is an SPDX checksum entry.
type Checksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

// ExternalRef is an SPDX external reference.
type ExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

// Relationship links two SPDX elements.
type Relationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

// ToolCreator returns the SPDX creator string for a zpkg version.
func ToolCreator(zpkgVersion string) string {
	if zpkgVersion == "" {
		zpkgVersion = "unknown"
	}
	return "Tool: zpkg-" + zpkgVersion
}

// PackageDocument builds a per-package SBOM from provenance.
func PackageDocument(info *provenance.Info) *Document {
	epoch := info.Build.SourceDateEpoch
	if epoch <= 0 {
		epoch = provenance.DefaultSourceDateEpoch
	}
	created := time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	doc := &Document{
		SPDXVersion:  Version,
		DataLicense:  "CC0-1.0",
		SPDXID:       DocumentID,
		Name:         "zignos-" + info.Package.Name + "-" + packageVersion(info),
		CreationInfo: CreationInfo{Created: created, Creators: []string{ToolCreator(info.Build.ZpkgVersion)}},
	}
	target := packageEntry(info)
	doc.Packages = append(doc.Packages, target)
	seen := map[string]bool{target.SPDXID: true}
	add := func(name, version, relation string) {
		id := PackageID(name, version)
		if id == target.SPDXID {
			return
		}
		if !seen[id] {
			seen[id] = true
			doc.Packages = append(doc.Packages, simplePackage(name, version))
		}
		from, to := id, target.SPDXID
		if relation == "DEPENDS_ON" {
			// The target package depends on its runtime dependency.
			from, to = target.SPDXID, id
		}
		doc.Relationships = append(doc.Relationships, Relationship{
			SPDXElementID: from, RelationshipType: relation, RelatedSPDXElement: to,
		})
	}
	for _, dep := range info.Dependencies {
		add(dep.Name, dep.Version, "DEPENDS_ON")
	}
	for _, dep := range info.BuildDeps {
		add(dep.Name, dep.Version, "BUILD_DEPENDENCY_OF")
	}
	for _, tool := range info.Toolchain {
		add(tool.Name, tool.Version, "BUILD_TOOL_OF")
	}
	doc.Relationships = append(doc.Relationships, Relationship{
		SPDXElementID: DocumentID, RelationshipType: "DESCRIBES", RelatedSPDXElement: target.SPDXID,
	})
	doc.DocumentNamespace = namespace(doc)
	return doc
}

// SystemDocument builds an SBOM for every installed package.
func SystemDocument(infos []*provenance.Info, zpkgVersion string) *Document {
	now := time.Now().UTC().Format(time.RFC3339)
	doc := &Document{
		SPDXVersion:  Version,
		DataLicense:  "CC0-1.0",
		SPDXID:       DocumentID,
		Name:         "zignos-system",
		CreationInfo: CreationInfo{Created: now, Creators: []string{ToolCreator(zpkgVersion)}},
	}
	system := Package{
		SPDXID: SystemID, Name: "ZIGNOS", VersionInfo: zpkgVersion,
		DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
		LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
	}
	doc.Packages = append(doc.Packages, system)
	doc.Relationships = append(doc.Relationships, Relationship{
		SPDXElementID: DocumentID, RelationshipType: "DESCRIBES", RelatedSPDXElement: SystemID,
	})
	byName := map[string]string{}
	sorted := append([]*provenance.Info(nil), infos...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Package.Name != sorted[j].Package.Name {
			return sorted[i].Package.Name < sorted[j].Package.Name
		}
		return sorted[i].Package.Version < sorted[j].Package.Version
	})
	for _, info := range sorted {
		p := packageEntry(info)
		doc.Packages = append(doc.Packages, p)
		byName[info.Package.Name] = p.SPDXID
		doc.Relationships = append(doc.Relationships, Relationship{
			SPDXElementID: SystemID, RelationshipType: "CONTAINS", RelatedSPDXElement: p.SPDXID,
		})
	}
	for _, info := range sorted {
		from := byName[info.Package.Name]
		for _, dep := range info.Dependencies {
			if to, ok := byName[dep.Name]; ok {
				doc.Relationships = append(doc.Relationships, Relationship{
					SPDXElementID: from, RelationshipType: "DEPENDS_ON", RelatedSPDXElement: to,
				})
			}
		}
		for _, dep := range info.BuildDeps {
			if to, ok := byName[dep.Name]; ok {
				doc.Relationships = append(doc.Relationships, Relationship{
					SPDXElementID: to, RelationshipType: "BUILD_DEPENDENCY_OF", RelatedSPDXElement: from,
				})
			}
		}
	}
	doc.DocumentNamespace = namespace(doc)
	return doc
}

// PackageID returns a stable SPDXID for a package name/version pair.
func PackageID(name, version string) string {
	sum := sha256.Sum256([]byte(name + "@" + version))
	return "SPDXRef-Pkg-" + hex.EncodeToString(sum[:8])
}

// Write writes an SPDX document as indented JSON, atomically.
func Write(path string, doc *Document) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func packageEntry(info *provenance.Info) Package {
	version := packageVersion(info)
	p := Package{
		SPDXID:           PackageID(info.Package.Name, version),
		Name:             info.Package.Name,
		VersionInfo:      version,
		DownloadLocation: "NOASSERTION",
		FilesAnalyzed:    false,
		LicenseConcluded: "NOASSERTION",
		LicenseDeclared:  "NOASSERTION",
		CopyrightText:    "NOASSERTION",
		PackageFileName:  info.Artifact.Filename,
		ExternalRefs: []ExternalRef{{
			ReferenceCategory: "PACKAGE-MANAGER",
			ReferenceType:     "purl",
			ReferenceLocator:  purl(info.Package.Name, version),
		}},
	}
	if len(info.Sources) > 0 {
		p.DownloadLocation = info.Sources[0].URL
	}
	if info.Artifact.SHA256 != "" {
		p.Checksums = append(p.Checksums, Checksum{Algorithm: "SHA256", ChecksumValue: info.Artifact.SHA256})
	}
	if info.Recipe.Digest != "" {
		p.SourceInfo = "recipe digest " + info.Recipe.Digest
	}
	return p
}

func simplePackage(name, version string) Package {
	if version == "" {
		version = "unknown"
	}
	return Package{
		SPDXID:           PackageID(name, version),
		Name:             name,
		VersionInfo:      version,
		DownloadLocation: "NOASSERTION",
		FilesAnalyzed:    false,
		LicenseConcluded: "NOASSERTION",
		LicenseDeclared:  "NOASSERTION",
		CopyrightText:    "NOASSERTION",
		ExternalRefs: []ExternalRef{{
			ReferenceCategory: "PACKAGE-MANAGER",
			ReferenceType:     "purl",
			ReferenceLocator:  purl(name, version),
		}},
	}
}

func packageVersion(info *provenance.Info) string {
	v := info.Package.Version
	if info.Package.Revision > 0 {
		v += "-r" + itoa(info.Package.Revision)
	}
	return v
}

func purl(name, version string) string {
	return "pkg:generic/zignos/" + url.PathEscape(name) + "@" + url.PathEscape(version)
}

func namespace(doc *Document) string {
	ids := make([]string, 0, len(doc.Packages))
	for _, p := range doc.Packages {
		ids = append(ids, p.SPDXID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(doc.Name + "\n" + doc.CreationInfo.Created + "\n" + strings.Join(ids, "\n")))
	return "https://studio-nosa-tech.github.io/spdx/" + hex.EncodeToString(sum[:16])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
