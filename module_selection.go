package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

const (
	moduleSelectionCommandTimeout  = 5 * time.Minute
	moduleSelectionDownloadLimit   = 16 << 20
	moduleSelectionModuleLimit     = 32 << 20
	moduleSelectionPackageLimit    = 128 << 20
	moduleSelectionMaxGraphModules = 10000
	moduleSelectionMaxPackages     = 250000
)

var fullCommitIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type moduleSelectionRequest struct {
	Path          string `json:"path"`
	RequestedPin  string `json:"requested_pin"`
	Resolved      string `json:"resolved_version,omitempty"`
	CommitRequest bool   `json:"commit_request,omitempty"`
}

type moduleSelectionGraphModule struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Main     bool   `json:"main,omitempty"`
	Indirect bool   `json:"indirect,omitempty"`
	Replace  *struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	} `json:"replace,omitempty"`
}

type moduleSelectionGoPackage struct {
	ImportPath   string                        `json:"ImportPath"`
	Name         string                        `json:"Name"`
	ForTest      string                        `json:"ForTest"`
	Incomplete   bool                          `json:"Incomplete"`
	Imports      []string                      `json:"Imports"`
	TestImports  []string                      `json:"TestImports"`
	XTestImports []string                      `json:"XTestImports"`
	Module       *moduleSelectionGraphModule   `json:"Module"`
	Error        *moduleSelectionPackageError  `json:"Error"`
	DepsErrors   []moduleSelectionPackageError `json:"DepsErrors"`
}

type moduleSelectionPackageError struct {
	ImportStack []string `json:"ImportStack"`
	Pos         string   `json:"Pos"`
	Err         string   `json:"Err"`
}

type moduleSelectionRequirement struct {
	Version  string
	Indirect bool
}

type moduleSelectionArchitectureEvidence struct {
	Architecture      string   `json:"architecture"`
	ProductionImports []string `json:"production_imports,omitempty"`
	TestImports       []string `json:"test_imports,omitempty"`
	TransitiveImports []string `json:"transitive_imports,omitempty"`
}

type moduleSelectionCandidate struct {
	Path                  string                                `json:"path"`
	DeclaredRequirement   string                                `json:"declared_requirement,omitempty"`
	RequirementDirectness string                                `json:"requirement_directness"`
	SelectedVersion       string                                `json:"selected_version"`
	Usage                 []string                              `json:"usage"`
	Architectures         []moduleSelectionArchitectureEvidence `json:"architectures,omitempty"`
}

type moduleSelectionReport struct {
	Schema        int                        `json:"schema"`
	Project       moduleSelectionRequest     `json:"project"`
	PackageScope  []string                   `json:"package_scope"`
	Architectures []string                   `json:"architectures"`
	Candidates    []moduleSelectionCandidate `json:"candidates"`
	Notes         []string                   `json:"notes"`
}

type moduleSelectionUse struct {
	production map[string]bool
	tests      map[string]bool
	transitive map[string]bool
}

type boundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buffer.Write(p[:remaining])
	}
	if remaining < len(p) {
		b.exceeded = true
	}
	return len(p), nil
}

func parseModuleSelectionRequest(raw string) (moduleSelectionRequest, error) {
	value := strings.TrimSpace(raw)
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return moduleSelectionRequest{}, fmt.Errorf("project must be specified as module@canonical-version or module@full-commit-sha")
	}
	path, pin := value[:at], value[at+1:]
	if err := module.CheckImportPath(path); err != nil {
		return moduleSelectionRequest{}, fmt.Errorf("invalid project module path: %w", err)
	}
	request := moduleSelectionRequest{Path: path, RequestedPin: pin}
	if fullCommitIDPattern.MatchString(pin) {
		request.CommitRequest = true
		return request, nil
	}
	if module.CanonicalVersion(pin) != pin {
		return moduleSelectionRequest{}, fmt.Errorf("project revision %q is floating or non-canonical; use a canonical Go version or full commit SHA", pin)
	}
	if err := module.Check(path, pin); err != nil {
		return moduleSelectionRequest{}, fmt.Errorf("invalid pinned Go module version: %w", err)
	}
	return request, nil
}

func validateModuleSelectionPackages(root string, packages []string) ([]string, error) {
	if len(packages) == 0 {
		return []string{"./..."}, nil
	}
	result := slices.Clone(packages)
	for _, pkg := range result {
		if err := module.CheckImportPath(pkg); err != nil {
			return nil, fmt.Errorf("invalid --package import path %q: %w", pkg, err)
		}
		if !importWithin(pkg, root) {
			return nil, fmt.Errorf("--package %q is outside project module %q", pkg, root)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func moduleSelectionGoEnv(workRoot, arch string) []string {
	env := combinedGoEnv(workRoot)
	filtered := make([]string, 0, len(env)+6)
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if key == "GOOS" || key == "GOARCH" || key == "CGO_ENABLED" || key == "GOAMD64" || key == "GORISCV64" || key == "GOFLAGS" || key == "GOCACHE" {
			continue
		}
		filtered = append(filtered, value)
	}
	filtered = append(filtered, "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=1", "GOAMD64=v1", "GORISCV64=rva20u64", "GOCACHE="+filepath.Join(workRoot, ".gocache"), "GOFLAGS=-mod=mod")
	return filtered
}

func runModuleSelectionGo(ctx context.Context, dir string, env []string, outputLimit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, moduleSelectionCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = env
	stdout := &boundedOutput{limit: outputLimit}
	stderr := &boundedOutput{limit: 1 << 20}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("go %s timed out or was canceled", args[0])
		}
		if stdout.exceeded || stderr.exceeded {
			return nil, fmt.Errorf("go %s output exceeded configured safety limit", args[0])
		}
		return nil, fmt.Errorf("go %s failed; check the pinned project and Go module network configuration", args[0])
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("go %s output exceeded configured safety limit", args[0])
	}
	return bytes.Clone(stdout.buffer.Bytes()), nil
}

func validateModuleSelectionResolvedPin(request moduleSelectionRequest, version string, origins ...*moduleOrigin) error {
	if version == "" || module.CanonicalVersion(version) != version {
		return fmt.Errorf("resolved project version is not canonical")
	}
	if request.CommitRequest {
		if !module.IsPseudoVersion(version) {
			return fmt.Errorf("full commit SHA did not resolve to a canonical Go pseudo-version")
		}
		revision, err := module.PseudoVersionRev(version)
		if err != nil || !strings.HasPrefix(request.RequestedPin, revision) {
			return fmt.Errorf("resolved pseudo-version does not identify the requested commit")
		}
		fullOriginMatch := false
		for _, origin := range origins {
			if origin == nil {
				continue
			}
			if err := checkModuleOrigin(version, origin); err != nil {
				return err
			}
			if len(origin.Hash) == len(request.RequestedPin) && strings.EqualFold(origin.Hash, request.RequestedPin) {
				fullOriginMatch = true
			}
		}
		if !fullOriginMatch {
			return fmt.Errorf("Go did not provide a matching full VCS origin hash for the requested commit")
		}
		return nil
	}
	if version != request.RequestedPin {
		return fmt.Errorf("Go resolved a version different from the explicitly requested project version")
	}
	for _, origin := range origins {
		if err := checkModuleOrigin(version, origin); err != nil {
			return err
		}
	}
	return nil
}

// downloadModuleSelectionProject validates the pinned module ZIP and extracts
// it into a disposable source tree before any Go package analysis is run.
func downloadModuleSelectionProject(ctx context.Context, work string, request moduleSelectionRequest) (moduleSelectionRequest, string, *modfile.File, error) {
	bootstrap := []byte("module go2spec.invalid/module-select\n\ngo 1.16\n")
	bootstrapPath := filepath.Join(work, "go.mod")
	if err := os.WriteFile(bootstrapPath, bootstrap, 0600); err != nil {
		return request, "", nil, fmt.Errorf("prepare isolated module resolver: %w", err)
	}
	arg := request.Path + "@" + request.RequestedPin
	data, err := runModuleSelectionGo(ctx, work, combinedGoEnv(work), moduleSelectionDownloadLimit, "mod", "download", "-json", arg)
	if err != nil {
		return request, "", nil, fmt.Errorf("download pinned project: %w", err)
	}
	var result moduleDownload
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&result); err != nil {
		return request, "", nil, fmt.Errorf("decode pinned project download metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return request, "", nil, fmt.Errorf("pinned project download returned unexpected trailing data")
	}
	if result.Error != "" || result.Path != request.Path || result.Version == "" || module.CanonicalVersion(result.Version) != result.Version {
		return request, "", nil, fmt.Errorf("pinned project resolved to an invalid module identity or version")
	}
	for _, item := range []struct {
		path  string
		limit int64
	}{{result.Info, 1 << 20}, {result.GoMod, modzip.MaxGoMod}, {result.Zip, modzip.MaxZipFile}} {
		if err := moduleCacheFile(work, item.path, item.limit); err != nil {
			return request, "", nil, fmt.Errorf("validate pinned project artifact: %w", err)
		}
	}
	infoBytes, err := os.ReadFile(result.Info)
	if err != nil {
		return request, "", nil, fmt.Errorf("read pinned project version metadata: %w", err)
	}
	var info struct {
		Version string
		Origin  *moduleOrigin
	}
	if err := json.Unmarshal(infoBytes, &info); err != nil || info.Version != result.Version {
		return request, "", nil, fmt.Errorf("pinned project .info identity/version does not match the resolved version")
	}
	if err := validateModuleSelectionResolvedPin(request, result.Version, result.Origin, info.Origin); err != nil {
		return request, "", nil, fmt.Errorf("resolved project pin verification failed: %w", err)
	}
	request.Resolved = result.Version
	goModBytes, err := os.ReadFile(result.GoMod)
	if err != nil {
		return request, "", nil, err
	}
	parsed, err := modfile.Parse("go.mod", goModBytes, nil)
	if err != nil {
		return request, "", nil, fmt.Errorf("parse pinned project go.mod: %w", err)
	}
	if parsed.Module == nil || parsed.Module.Mod.Path != request.Path {
		return request, "", nil, fmt.Errorf("pinned project go.mod module path does not match %q", request.Path)
	}
	if len(parsed.Replace) > 0 || len(parsed.Exclude) > 0 {
		return request, "", nil, fmt.Errorf("project go.mod contains replace or exclude directives; this analysis mode does not support them")
	}
	resolved := module.Version{Path: request.Path, Version: result.Version}
	if err := module.Check(resolved.Path, resolved.Version); err != nil {
		return request, "", nil, fmt.Errorf("resolved project version is invalid: %w", err)
	}
	if _, err := modzip.CheckZip(resolved, result.Zip); err != nil {
		return request, "", nil, fmt.Errorf("validate pinned project ZIP: %w", err)
	}
	if err := checkModuleZipExtraction(result.Zip); err != nil {
		return request, "", nil, fmt.Errorf("validate pinned project ZIP extraction: %w", err)
	}
	sourceDir := filepath.Join(work, "project")
	if err := modzip.Unzip(sourceDir, resolved, result.Zip); err != nil {
		return request, "", nil, fmt.Errorf("extract pinned project: %w", err)
	}
	installedGoMod, err := os.ReadFile(filepath.Join(sourceDir, "go.mod"))
	if err != nil {
		return request, "", nil, fmt.Errorf("read extracted project go.mod: %w", err)
	}
	if !bytes.Equal(installedGoMod, goModBytes) {
		return request, "", nil, fmt.Errorf("go.mod in pinned ZIP differs from verified module metadata")
	}
	if err := makeModuleSelectionTreeWritable(sourceDir); err != nil {
		return request, "", nil, fmt.Errorf("prepare disposable project source for Go analysis: %w", err)
	}
	return request, sourceDir, parsed, nil
}

func makeModuleSelectionTreeWritable(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported extracted project file type at %q", path)
		}
		mode := info.Mode().Perm() | 0200
		if info.IsDir() {
			mode |= 0100
		}
		return os.Chmod(path, mode)
	})
}

func decodeModuleSelectionModules(data []byte) ([]moduleSelectionGraphModule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var modules []moduleSelectionGraphModule
	for {
		var current moduleSelectionGraphModule
		if err := decoder.Decode(&current); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode Go module graph: %w", err)
		}
		if current.Path == "" {
			return nil, fmt.Errorf("Go module graph contains a record without a path")
		}
		modules = append(modules, current)
		if len(modules) > moduleSelectionMaxGraphModules {
			return nil, fmt.Errorf("Go module graph exceeds the %d-module analysis limit", moduleSelectionMaxGraphModules)
		}
	}
	return modules, nil
}

func decodeModuleSelectionPackages(data []byte) ([]moduleSelectionGoPackage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var packages []moduleSelectionGoPackage
	for {
		var current moduleSelectionGoPackage
		if err := decoder.Decode(&current); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode Go package analysis: %w", err)
		}
		if current.Error != nil && current.Error.Err != "" {
			return nil, fmt.Errorf("Go package analysis reported an error for the pinned project")
		}
		if current.Incomplete || len(current.DepsErrors) != 0 {
			return nil, fmt.Errorf("Go package analysis is incomplete for the pinned project")
		}
		packages = append(packages, current)
		if len(packages) > moduleSelectionMaxPackages {
			return nil, fmt.Errorf("Go package analysis exceeds the %d-package limit", moduleSelectionMaxPackages)
		}
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("Go package analysis found no packages in the selected project")
	}
	return packages, nil
}

func moduleSelectionRequirements(parsed *modfile.File) map[string]moduleSelectionRequirement {
	requirements := make(map[string]moduleSelectionRequirement, len(parsed.Require))
	for _, requirement := range parsed.Require {
		requirements[requirement.Mod.Path] = moduleSelectionRequirement{Version: requirement.Mod.Version, Indirect: requirement.Indirect}
	}
	return requirements
}

func analyzeModuleSelectionArchitecture(ctx context.Context, sourceDir, root string, packages []string, arch string) ([]moduleSelectionGoPackage, error) {
	args := []string{"list", "-deps", "-test", "-json"}
	args = append(args, packages...)
	data, err := runModuleSelectionGo(ctx, sourceDir, moduleSelectionGoEnv(filepath.Dir(sourceDir), arch), moduleSelectionPackageLimit, args...)
	if err != nil {
		return nil, fmt.Errorf("analyze project packages for linux/%s: %w", arch, err)
	}
	results, err := decodeModuleSelectionPackages(data)
	if err != nil {
		return nil, err
	}
	rootPackages := 0
	for _, pkg := range results {
		if pkg.Module != nil && pkg.Module.Main && pkg.Module.Path == root && pkg.ForTest == "" && pkg.ImportPath != "" {
			rootPackages++
		}
	}
	if rootPackages == 0 {
		return nil, fmt.Errorf("no packages from the pinned root module were selected for linux/%s", arch)
	}
	return results, nil
}

func moduleSelectionGraph(ctx context.Context, sourceDir string) ([]moduleSelectionGraphModule, error) {
	data, err := runModuleSelectionGo(ctx, sourceDir, moduleSelectionGoEnv(filepath.Dir(sourceDir), "amd64"), moduleSelectionModuleLimit, "list", "-m", "-json", "all")
	if err != nil {
		return nil, fmt.Errorf("resolve Go module graph: %w", err)
	}
	modules, err := decodeModuleSelectionModules(data)
	if err != nil {
		return nil, err
	}
	for _, current := range modules {
		if current.Replace != nil {
			return nil, fmt.Errorf("Go module graph contains a replacement for %q; replacement semantics are unsupported", current.Path)
		}
	}
	return modules, nil
}

func moduleSelectionCanonicalImportPath(importPath string) string {
	if index := strings.Index(importPath, " ["); index > 0 && strings.HasSuffix(importPath, "]") {
		return importPath[:index]
	}
	return importPath
}

func moduleOwnerForImport(importPath string, packageOwners map[string]*moduleSelectionGraphModule, root string) (string, bool) {
	importPath = moduleSelectionCanonicalImportPath(importPath)
	if importPath == "" || importPath == "C" {
		return "", true
	}
	owner, found := packageOwners[importPath]
	if !found {
		return "", false
	}
	if owner == nil || owner.Main || owner.Path == root {
		return "", true
	}
	return owner.Path, true
}

func addSelectionPackage(set map[string]bool, pkg string) {
	if pkg != "" {
		set[pkg] = true
	}
}

func sortedSelectionSet(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}

// moduleSelectionCandidates reports graph modules with Go package-import evidence.
// Module ownership comes from go list package records, not import-path prefixes.
func moduleSelectionCandidates(root string, graph []moduleSelectionGraphModule, requirements map[string]moduleSelectionRequirement, byArch map[string][]moduleSelectionGoPackage) ([]moduleSelectionCandidate, error) {
	graphByPath := make(map[string]moduleSelectionGraphModule, len(graph))
	rootFound := false
	for _, current := range graph {
		if current.Main {
			if current.Path != root {
				return nil, fmt.Errorf("Go module graph main module %q does not match pinned project %q", current.Path, root)
			}
			rootFound = true
			continue
		}
		if current.Version == "" || module.CanonicalVersion(current.Version) != current.Version {
			return nil, fmt.Errorf("module graph contains an invalid selected version for %q", current.Path)
		}
		if _, exists := graphByPath[current.Path]; exists {
			return nil, fmt.Errorf("module graph contains duplicate module path %q", current.Path)
		}
		graphByPath[current.Path] = current
	}
	if !rootFound {
		return nil, fmt.Errorf("Go module graph does not identify the pinned project as its main module")
	}
	uses := make(map[string]map[string]*moduleSelectionUse, len(graphByPath))
	for path := range graphByPath {
		uses[path] = make(map[string]*moduleSelectionUse, len(byArch))
		for arch := range byArch {
			uses[path][arch] = &moduleSelectionUse{production: map[string]bool{}, tests: map[string]bool{}, transitive: map[string]bool{}}
		}
	}
	for arch, packages := range byArch {
		packageOwners := make(map[string]*moduleSelectionGraphModule, len(packages))
		for i := range packages {
			pkg := &packages[i]
			importPath := moduleSelectionCanonicalImportPath(pkg.ImportPath)
			if importPath == "" {
				continue
			}
			if existing, found := packageOwners[importPath]; found {
				if (existing == nil) != (pkg.Module == nil) || (existing != nil && pkg.Module != nil && (existing.Path != pkg.Module.Path || existing.Version != pkg.Module.Version)) {
					return nil, fmt.Errorf("Go package %q has conflicting module ownership", importPath)
				}
				continue
			}
			if pkg.Module == nil {
				packageOwners[importPath] = nil
				continue
			}
			owner := *pkg.Module
			packageOwners[importPath] = &owner
		}
		markImport := func(importPath, useKind string) error {
			importPath = moduleSelectionCanonicalImportPath(importPath)
			modulePath, found := moduleOwnerForImport(importPath, packageOwners, root)
			if !found {
				return fmt.Errorf("Go package analysis did not report the owner of imported package %q", importPath)
			}
			if modulePath == "" {
				return nil
			}
			selected, exists := graphByPath[modulePath]
			if !exists {
				return fmt.Errorf("imported package %q belongs to module %q absent from the Go module graph", importPath, modulePath)
			}
			owner := packageOwners[importPath]
			if owner.Version != "" && owner.Version != selected.Version {
				return fmt.Errorf("imported package %q module version disagrees with Go's selected graph", importPath)
			}
			use := uses[modulePath][arch]
			switch useKind {
			case "production":
				addSelectionPackage(use.production, importPath)
			case "test":
				addSelectionPackage(use.tests, importPath)
			case "transitive":
				addSelectionPackage(use.transitive, importPath)
			default:
				return fmt.Errorf("unsupported module usage category %q", useKind)
			}
			return nil
		}
		for _, pkg := range packages {
			if pkg.Module == nil || !pkg.Module.Main || pkg.Module.Path != root || pkg.ForTest != "" {
				continue
			}
			for _, imported := range pkg.Imports {
				if err := markImport(imported, "production"); err != nil {
					return nil, err
				}
			}
			for _, imported := range append(slices.Clone(pkg.TestImports), pkg.XTestImports...) {
				modulePath, found := moduleOwnerForImport(imported, packageOwners, root)
				if !found {
					return nil, fmt.Errorf("Go package analysis did not report the owner of imported package %q", imported)
				}
				if modulePath == "" {
					continue
				}
				if err := markImport(imported, "test"); err != nil {
					return nil, err
				}
			}
		}
		// Imports from dependency-owned packages identify transitive modules;
		// intra-module imports are not separate source candidates.
		for _, pkg := range packages {
			if pkg.Module == nil || pkg.Module.Main || pkg.Module.Path == root || pkg.ForTest != "" {
				continue
			}
			for _, imported := range pkg.Imports {
				modulePath, found := moduleOwnerForImport(imported, packageOwners, root)
				if !found {
					return nil, fmt.Errorf("Go package analysis did not report the owner of imported package %q", imported)
				}
				if modulePath == "" || modulePath == pkg.Module.Path {
					continue
				}
				if err := markImport(imported, "transitive"); err != nil {
					return nil, err
				}
			}
		}
	}
	paths := make([]string, 0, len(graphByPath))
	for path := range graphByPath {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	arches := make([]string, 0, len(byArch))
	for arch := range byArch {
		arches = append(arches, arch)
	}
	slices.Sort(arches)
	candidates := make([]moduleSelectionCandidate, 0, len(paths))
	for _, path := range paths {
		selected := graphByPath[path]
		requirement, declared := requirements[path]
		directness := "not-declared-in-root-go.mod"
		declaredVersion := ""
		if declared {
			declaredVersion = requirement.Version
			if requirement.Indirect {
				directness = "indirect"
			} else {
				directness = "direct"
			}
		}
		var usage []string
		var archEvidence []moduleSelectionArchitectureEvidence
		for _, arch := range arches {
			use := uses[path][arch]
			production := sortedSelectionSet(use.production)
			tests := sortedSelectionSet(use.tests)
			transitive := sortedSelectionSet(use.transitive)
			if len(production) != 0 && !slices.Contains(usage, "production-import") {
				usage = append(usage, "production-import")
			}
			if len(tests) != 0 && !slices.Contains(usage, "test-import") {
				usage = append(usage, "test-import")
			}
			if len(transitive) != 0 && !slices.Contains(usage, "transitive") {
				usage = append(usage, "transitive")
			}
			if len(production)+len(tests)+len(transitive) != 0 {
				archEvidence = append(archEvidence, moduleSelectionArchitectureEvidence{
					Architecture: arch, ProductionImports: production,
					TestImports: tests, TransitiveImports: transitive,
				})
			}
		}
		if len(usage) == 0 {
			if declared {
				usage = append(usage, "declared-unused")
			} else {
				usage = append(usage, "graph-only")
			}
		}
		candidates = append(candidates, moduleSelectionCandidate{
			Path: path, DeclaredRequirement: declaredVersion, RequirementDirectness: directness,
			SelectedVersion: selected.Version, Usage: usage, Architectures: archEvidence,
		})
	}
	return candidates, nil
}

func moduleSelectionReportFor(ctx context.Context, work string, request moduleSelectionRequest, packageSelectors []string) (moduleSelectionReport, error) {
	var report moduleSelectionReport
	request, sourceDir, parsed, err := downloadModuleSelectionProject(ctx, work, request)
	if err != nil {
		return report, err
	}
	patterns, err := validateModuleSelectionPackages(request.Path, packageSelectors)
	if err != nil {
		return report, err
	}
	byArch := make(map[string][]moduleSelectionGoPackage, 2)
	for _, arch := range []string{"amd64", "riscv64"} {
		packages, err := analyzeModuleSelectionArchitecture(ctx, sourceDir, request.Path, patterns, arch)
		if err != nil {
			return report, err
		}
		byArch[arch] = packages
	}
	graph, err := moduleSelectionGraph(ctx, sourceDir)
	if err != nil {
		return report, err
	}
	candidates, err := moduleSelectionCandidates(request.Path, graph, moduleSelectionRequirements(parsed), byArch)
	if err != nil {
		return report, err
	}
	return moduleSelectionReport{
		Schema: 1, Project: request, PackageScope: patterns,
		Architectures: []string{"amd64", "riscv64"}, Candidates: candidates,
		Notes: []string{
			"Candidates are advisory; maintainers choose the final pack -combine source list.",
			"Go's selected module versions come from the pinned project's module graph; no API compatibility is inferred.",
			"Nested go.mod modules are not scanned by the default ./... scope unless selected packages import them as dependencies.",
			"This command does not edit or update RPM specs.",
		},
	}, nil
}

func moduleSelectionReportJSON(report moduleSelectionReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func renderModuleSelectionReport(report moduleSelectionReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s@%s", report.Project.Path, report.Project.RequestedPin)
	if report.Project.Resolved != "" && report.Project.Resolved != report.Project.RequestedPin {
		fmt.Fprintf(&b, " (resolved %s)", report.Project.Resolved)
	}
	fmt.Fprintf(&b, "\nPackage scope: %s\nArchitectures: %s\n", strings.Join(report.PackageScope, ", "), strings.Join(report.Architectures, ", "))
	for _, candidate := range report.Candidates {
		fmt.Fprintf(&b, "\n[%s] %s@%s", strings.Join(candidate.Usage, ","), candidate.Path, candidate.SelectedVersion)
		if candidate.DeclaredRequirement != "" {
			fmt.Fprintf(&b, " (root go.mod %s: %s)", candidate.RequirementDirectness, candidate.DeclaredRequirement)
		} else {
			fmt.Fprintf(&b, " (%s)", candidate.RequirementDirectness)
		}
		for _, evidence := range candidate.Architectures {
			fmt.Fprintf(&b, "\n  linux/%s", evidence.Architecture)
			if len(evidence.ProductionImports) != 0 {
				fmt.Fprintf(&b, " production imports: %s", strings.Join(evidence.ProductionImports, ", "))
			}
			if len(evidence.TestImports) != 0 {
				fmt.Fprintf(&b, " test imports: %s", strings.Join(evidence.TestImports, ", "))
			}
			if len(evidence.TransitiveImports) != 0 {
				fmt.Fprintf(&b, " transitive imports: %s", strings.Join(evidence.TransitiveImports, ", "))
			}
		}
	}
	for _, note := range report.Notes {
		fmt.Fprintf(&b, "\n\nNote: %s", note)
	}
	return b.String()
}
