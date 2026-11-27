package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/build"
	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/depspec"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/spdx"
)

// zpkgVersion reports the installed zpkg version for provenance/SBOM
// creators. When zpkg is not installed in the target root (early bootstrap)
// it falls back to the release this binary was cut from.
func zpkgVersion(cfg config.Config) string {
	if data, err := os.ReadFile(filepath.Join(cfg.DB, "zpkg", "version")); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	return Version
}

// depComponents resolves dependency atoms to installed name/version pairs.
// Optional dependencies guarded by a disabled USE flag are skipped. The
// version is the installed one, which is what actually built the package.
func depComponents(db pkgdb.DB, atoms, use []string) []provenance.Component {
	useSet := map[string]bool{}
	for _, f := range use {
		useSet[f] = true
	}
	seen := map[string]bool{}
	var out []provenance.Component
	for _, raw := range atoms {
		a, err := depspec.Parse(raw)
		if err != nil {
			continue
		}
		if a.Condition != "" {
			enabled := useSet[a.Condition]
			if a.Negated {
				enabled = !enabled
			}
			if !enabled {
				continue
			}
		}
		if a.Name == "" || seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		rec, err := db.Get(a.Name, "0")
		if err != nil {
			recs, lerr := db.GetAll(a.Name)
			if lerr != nil || len(recs) == 0 {
				continue
			}
			rec = recs[0]
		}
		out = append(out, provenance.Component{Name: a.Name, Version: rec.VersionString()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// provenanceForBuild assembles the provenance record for an installed or
// packed package. res carries source-build details; meta carries origin and
// environment information for every origin kind.
func provenanceForBuild(db pkgdb.DB, cfg config.Config, r *recipe.Recipe, res *build.Result, meta *pkgdb.Meta) *provenance.Info {
	opts := cfg.Options.ForPackage(r.Name)
	base := build.PackageBase(r, opts.HashFor(r.UsesVideoCards()))
	info := &provenance.Info{
		Schema: provenance.Schema,
		Package: provenance.Package{
			Name: r.Name, Version: r.Version, Revision: r.Revision,
			Slot: r.Slot, Arch: runtime.GOARCH,
		},
		Recipe: provenance.Recipe{
			Repo: r.Repo, Dir: r.Dir, Overlay: cfg.OverlayName(r.Repo),
			Channel: cfg.Channel, Digest: res.RecipeDigest,
			KeyID: meta.OriginKeyID, Generation: meta.OriginGeneration,
		},
		Sources: res.Sources,
		Build: provenance.Build{
			Use:              append([]string{}, meta.UseFlags...),
			VideoCards:       append([]string{}, meta.VideoCards...),
			CFlags:           opts.CFlags,
			MakeOpts:         opts.MakeOpts,
			Sandbox:          meta.BuildSandbox,
			Network:          meta.BuildNetwork,
			SourceDateEpoch:  res.SourceDateEpoch,
			Started:          res.Started,
			Finished:         res.Finished,
			Origin:           meta.Origin,
			OriginRepo:       meta.OriginRepo,
			OriginKeyID:      meta.OriginKeyID,
			OriginGeneration: meta.OriginGeneration,
			ZpkgVersion:      zpkgVersion(cfg),
		},
		Environment: provenance.Environment{Hash: meta.BuildEnvHash, Count: meta.BuildEnvCount},
		Artifact: provenance.Artifact{
			Filename:       base + ".pkg.tar.gz",
			SHA256:         res.ArtifactSHA256,
			Size:           res.ArtifactSize,
			ManifestSHA256: res.ManifestSHA256,
		},
		Log: provenance.Log{Path: res.LogPath, SHA256: res.LogSHA256},
	}
	if res.OriginKind == "source" {
		pkgs, _, _ := provenance.InstalledEnvironment(db)
		info.Environment.Packages = pkgs
		info.Toolchain = provenance.Toolchain(db)
		info.Dependencies = depComponents(db, r.Depends, opts.Use)
		info.BuildDeps = depComponents(db, r.MakeDepends, opts.Use)
		root, keyID, generation := recipeTreeOrigin(r, cfg)
		if keyID != "" {
			info.Recipe.KeyID = keyID
			info.Recipe.Generation = generation
			if info.Build.OriginRepo == "" || info.Build.OriginRepo == root {
				info.Build.OriginRepo = root
				info.Build.OriginKeyID = keyID
				info.Build.OriginGeneration = generation
			}
		}
	} else {
		// The binpkg sidecar preserves the source build's digest and log;
		// sources and toolchain are not distributed with binary packages.
		if res.LogSHA256 != "" && info.Log.Path == "" {
			info.Log.Path = filepath.Join(cfg.Cache, "logs", base+".log")
		}
		if info.Artifact.SHA256 == "" {
			info.Artifact.SHA256 = res.OriginPkgSHA256
		}
	}
	return info
}

// writeArtifactProvenance stores the provenance/SBOM sidecars next to a
// freshly packed binary package.
func writeArtifactProvenance(cfg config.Config, res *build.Result, info *provenance.Info) error {
	if res.ArtifactPath == "" {
		return nil
	}
	base := strings.TrimSuffix(res.ArtifactPath, ".pkg.tar.gz")
	if err := provenance.Write(base+".build.json", info); err != nil {
		return err
	}
	if cfg.SBOM {
		if err := spdx.Write(base+".spdx.json", spdx.PackageDocument(info)); err != nil {
			return err
		}
	}
	return nil
}

// writeInstalledProvenance stores provenance and the per-package SBOM in the
// package database record directory.
func writeInstalledProvenance(cfg config.Config, db pkgdb.DB, r *recipe.Recipe, info *provenance.Info) error {
	dir := filepath.Join(db.Dir, pkgdb.SlotKey(r.Name, r.Slot))
	if err := provenance.Write(filepath.Join(dir, "provenance.json"), info); err != nil {
		return err
	}
	if cfg.SBOM {
		if err := spdx.Write(filepath.Join(dir, "sbom.json"), spdx.PackageDocument(info)); err != nil {
			return err
		}
	}
	return nil
}

// diffEntries compares two destroot inventories and returns a sorted list of
// differences. It is the reproducibility check's comparison step.
func diffEntries(a, b []manifest.Entry) []string {
	am := make(map[string]manifest.Entry, len(a))
	for _, e := range a {
		am[e.Path] = e
	}
	bm := make(map[string]manifest.Entry, len(b))
	for _, e := range b {
		bm[e.Path] = e
	}
	var diffs []string
	for path, e := range am {
		other, ok := bm[path]
		if !ok {
			diffs = append(diffs, "- "+path)
			continue
		}
		if e.Kind != other.Kind || e.Hash != other.Hash || e.Link != other.Link {
			diffs = append(diffs, "~ "+path)
		}
	}
	for path := range bm {
		if _, ok := am[path]; !ok {
			diffs = append(diffs, "+ "+path)
		}
	}
	sort.Strings(diffs)
	return diffs
}

// cmdProvenance prints the stored provenance for one or more packages.
func cmdProvenance(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	for _, arg := range args {
		name, slot := pkgdb.SplitKey(arg)
		info, err := loadProvenance(db, name, slot)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return err
		}
		if len(args) > 1 {
			fmt.Printf("==> %s <==\n", arg)
		}
		fmt.Println(string(data))
	}
	return nil
}

// cmdSBOM prints an SPDX 2.3 document for packages or the whole system.
func cmdSBOM(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	system := false
	output := ""
	var names []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--system":
			system = true
		case "-o", "--output":
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a file name", a)
			}
			i++
			output = args[i]
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option %q", a)
			}
			names = append(names, a)
		}
	}
	if system && len(names) > 0 {
		return fmt.Errorf("--system cannot be combined with package names")
	}
	if !system && len(names) == 0 {
		return fmt.Errorf("usage: zpkg sbom <pkg>... | zpkg sbom --system [-o <file>]")
	}
	if output != "" && len(names) > 1 {
		return fmt.Errorf("-o accepts one package when not using --system")
	}

	if system {
		infos, err := allProvenance(db)
		if err != nil {
			return err
		}
		doc := spdx.SystemDocument(infos, zpkgVersion(cfg))
		return emitSPDX(doc, output)
	}
	for _, name := range names {
		doc, err := packageSPDX(cfg, db, name)
		if err != nil {
			return err
		}
		if err := emitSPDX(doc, output); err != nil {
			return err
		}
	}
	return nil
}

func emitSPDX(doc *spdx.Document, output string) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if output == "" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(output, data, 0o644)
}

// packageSPDX returns the stored SBOM when present and otherwise derives one
// from provenance.
func packageSPDX(cfg config.Config, db pkgdb.DB, arg string) (*spdx.Document, error) {
	name, slot := pkgdb.SplitKey(arg)
	path := filepath.Join(db.Dir, pkgdb.SlotKey(name, slot), "sbom.json")
	if data, err := os.ReadFile(path); err == nil {
		var doc spdx.Document
		if err := json.Unmarshal(data, &doc); err == nil && doc.SPDXVersion != "" {
			return &doc, nil
		}
	}
	info, err := loadProvenance(db, name, slot)
	if err != nil {
		return nil, err
	}
	return spdx.PackageDocument(info), nil
}

func allProvenance(db pkgdb.DB) ([]*provenance.Info, error) {
	refs, err := db.List()
	if err != nil {
		return nil, err
	}
	var infos []*provenance.Info
	for _, ref := range refs {
		info, err := loadProvenance(db, ref.Name, ref.Slot)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func loadProvenance(db pkgdb.DB, name, slot string) (*provenance.Info, error) {
	path := filepath.Join(db.Dir, pkgdb.SlotKey(name, slot), "provenance.json")
	if info, err := provenance.Read(path); err == nil {
		return info, nil
	}
	rec, err := db.Get(name, slot)
	if err != nil {
		return nil, err
	}
	return provenance.FromMeta(rec), nil
}

// cmdLog prints a package's build log or its path.
func cmdLog(args []string) error {
	pathOnly := false
	var names []string
	for _, a := range args {
		switch a {
		case "-p", "--path":
			pathOnly = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option %q", a)
			}
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("usage: zpkg log [-p] <pkg>...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	for _, arg := range names {
		name, slot := pkgdb.SplitKey(arg)
		info, err := loadProvenance(db, name, slot)
		if err != nil {
			return err
		}
		if info.Log.Path == "" {
			return fmt.Errorf("%s: no build log recorded", arg)
		}
		if pathOnly {
			fmt.Println(info.Log.Path)
			continue
		}
		data, err := os.ReadFile(info.Log.Path)
		if err != nil {
			return err
		}
		if len(names) > 1 {
			fmt.Printf("==> %s <==\n", info.Log.Path)
		}
		os.Stdout.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Println()
		}
	}
	return nil
}
