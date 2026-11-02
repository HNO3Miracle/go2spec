package main

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"go/version"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type aggregatePackage struct {
	Path       string
	Library    bool
	Importable bool
	Imports    []string
	Tests      []string
}

func aggregateDependencies(ctx context.Context, work string, plan *aggregatePlan) error {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	cmd.Dir = work
	cmd.Env = combinedGoEnv(work)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("cannot determine the local Go toolchain")
	}
	if version.Compare(strings.TrimSpace(string(out)), "go"+plan.GoVersion) < 0 {
		return fmt.Errorf("selected modules require Go >= %s; automatic toolchain download is disabled", plan.GoVersion)
	}
	buildDeps, runDeps := make(map[string]bool), make(map[string]bool)
	var baselineBuild, baselineRun, baselineLibraries []string
	baselineSet := false
	for _, arch := range []string{"amd64", "riscv64"} {
		bc, std, err := aggregateBuildContext(ctx, work, arch)
		if err != nil {
			return err
		}
		archBuildDeps, archRunDeps := make(map[string]bool), make(map[string]bool)
		packages, err := aggregatePackages(*plan, bc)
		if err != nil {
			return err
		}
		// A noarch source RPM serves both openRuyi Go target architectures.
		// Dependency and library-package sets must therefore agree across them.
		libraries := make(map[string]bool)
		for p, pkg := range packages {
			if pkg.Library {
				libraries[p] = true
			}
		}

		// imports are runtime dependencies too, even without a helper Provides.
		runtimePackages := make(map[string]bool)
		var visit func(string)
		visit = func(p string) {
			if runtimePackages[p] {
				return
			}
			runtimePackages[p] = true
			for _, dep := range packages[p].Imports {
				if _, internal := packages[dep]; internal {
					visit(dep)
				}
			}
		}
		for p, pkg := range packages {
			if pkg.Library {
				visit(p)
			}
		}
		for _, pkg := range packages {
			for _, group := range []struct {
				imports []string
				runtime bool
			}{{pkg.Imports, runtimePackages[pkg.Path]}, {pkg.Tests, false}} {
				for _, dep := range group.imports {
					if !aggregateImportVisible(pkg.Path, dep) {
						return fmt.Errorf("%s: import %s violates Go internal visibility", pkg.Path, dep)
					}
					if std[dep] {
						continue
					}
					if internal, ok := packages[dep]; ok {
						if !internal.Importable {
							return fmt.Errorf("%s (%s): internal import %s is not an importable library", pkg.Path, arch, dep)
						}
						continue
					}
					if aggregateOwnsImport(*plan, dep) {
						return fmt.Errorf("%s (%s): selected source does not provide internal import %s", pkg.Path, arch, dep)
					}
					if !safeImportCapability(dep) {
						return fmt.Errorf("%s: unsupported external import %q", pkg.Path, dep)
					}
					archBuildDeps[dep] = true
					if group.runtime {
						archRunDeps[dep] = true
					}
				}
			}
		}
		archBuild := sortedDependencySet(archBuildDeps)
		archRun := sortedDependencySet(archRunDeps)
		archLibraries := make([]string, 0, len(libraries))
		for p := range libraries {
			archLibraries = append(archLibraries, p)
		}
		slices.Sort(archLibraries)
		if !baselineSet {
			baselineSet = true
			baselineBuild, baselineRun, baselineLibraries = archBuild, archRun, archLibraries
			buildDeps, runDeps = archBuildDeps, archRunDeps
		} else if !slices.Equal(baselineBuild, archBuild) || !slices.Equal(baselineRun, archRun) || !slices.Equal(baselineLibraries, archLibraries) {
			return fmt.Errorf("linux architecture dependency or library package sets differ; a noarch combined RPM cannot represent this safely")
		}
	}
	plan.BuildDeps = sortedDependencySet(buildDeps)
	plan.RunDeps = sortedDependencySet(runDeps)
	return nil
}

// A required nested module is external unless it was explicitly selected.
// A shared repository/import prefix alone does not make it part of this RPM.
func aggregateOwnsImport(plan aggregatePlan, dep string) bool {
	owner, selected := "", false
	for _, source := range plan.Sources {
		if importWithin(dep, source.Module.Path) && len(source.Module.Path) >= len(owner) {
			owner, selected = source.Module.Path, true
		}
		for _, required := range source.Requires {
			if importWithin(dep, required.Path) && len(required.Path) > len(owner) {
				owner, selected = required.Path, false
			}
		}
	}
	return selected
}

func aggregateBuildContext(ctx context.Context, work, arch string) (build.Context, map[string]bool, error) {
	bc := build.Default
	bc.GOOS, bc.GOARCH, bc.CgoEnabled = "linux", arch, true
	bc.BuildTags, bc.ReleaseTags, bc.ToolTags = nil, nil, nil
	// Use the installed Go command's tags, not the compiler that built go2spec.
	format := `{{if eq .ImportPath "runtime"}}#release {{join context.ReleaseTags " "}}
#tool {{join context.ToolTags " "}}
{{end}}{{.ImportPath}}`
	cmd := exec.CommandContext(ctx, "go", "list", "-f", format, "std")
	cmd.Dir = work
	env := combinedGoEnv(work)
	var filtered []string
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if key != "GOOS" && key != "GOARCH" && key != "CGO_ENABLED" && key != "GOAMD64" && key != "GORISCV64" {
			filtered = append(filtered, value)
		}
	}
	cmd.Env = append(filtered, "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=1", "GOAMD64=v1", "GORISCV64=rva20u64")
	out, err := cmd.Output()
	if err != nil {
		return bc, nil, fmt.Errorf("cannot enumerate standard Go packages for linux/%s", arch)
	}
	std := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "#release":
			bc.ReleaseTags = fields[1:]
		case "#tool":
			bc.ToolTags = fields[1:]
		default:
			std[line] = true
		}
	}
	if !std["fmt"] || !std["runtime"] || len(bc.ReleaseTags) == 0 || len(bc.ToolTags) == 0 {
		return bc, nil, fmt.Errorf("incomplete standard package inventory or build tags for linux/%s", arch)
	}
	return bc, std, nil
}

func aggregateImportVisible(importer, imported string) bool {
	parts := strings.Split(imported, "/")
	for i, component := range parts {
		if component == "internal" && (i == 0 || !importWithin(importer, strings.Join(parts[:i], "/"))) {
			return false
		}
	}
	return true
}

func safeImportCapability(p string) bool {
	return importRootPattern.MatchString(p) && !strings.HasPrefix(p, "/") &&
		!strings.HasSuffix(p, "/") && !strings.Contains(p, "//") &&
		path.Clean(p) == p && !build.IsLocalImport(p) && strings.Contains(strings.Split(p, "/")[0], ".")
}

func aggregatePackages(plan aggregatePlan, bc build.Context) (map[string]aggregatePackage, error) {
	packages := make(map[string]aggregatePackage)
	for _, source := range plan.Sources {
		err := filepath.WalkDir(source.Dir, func(dir string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			if dir != source.Dir && (entry.Name() == "vendor" || entry.Name() == "testdata" ||
				strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_")) {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(source.Dir, dir)
			if err != nil {
				return err
			}
			pkgPath := source.Module.Path
			if rel != "." {
				pkgPath += "/" + filepath.ToSlash(rel)
			}
			pkg, err := bc.ImportDir(dir, build.ImportComment)
			if err != nil {
				var empty *build.NoGoError
				if errors.As(err, &empty) {
					return nil
				}
				return fmt.Errorf("%s (%s): incomplete dependency discovery: %w", pkgPath, bc.GOARCH, err)
			}
			if len(pkg.CgoFiles) != 0 {
				return fmt.Errorf("%s (%s): Cgo native build dependencies require manual packaging; combine does not infer them", pkgPath, bc.GOARCH)
			}
			if pkg.ImportComment != "" && pkg.ImportComment != pkgPath {
				return fmt.Errorf("%s: canonical import comment %q differs from installation path", pkgPath, pkg.ImportComment)
			}
			if _, duplicate := packages[pkgPath]; duplicate {
				return fmt.Errorf("duplicate installed package %s", pkgPath)
			}
			relative := strings.TrimPrefix(pkgPath, plan.Request.Root)
			importable := pkg.Name != "main" && len(pkg.GoFiles)+len(pkg.SFiles) > 0
			library := importable
			for _, component := range strings.Split(relative, "/") {
				if component == "example" || component == "examples" {
					library = false
				}
			}
			packages[pkgPath] = aggregatePackage{
				Path: pkgPath, Library: library, Importable: importable, Imports: pkg.Imports,
				Tests: append(append([]string{}, pkg.TestImports...), pkg.XTestImports...),
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("selected sources contain no packages for linux/%s", bc.GOARCH)
	}
	return packages, nil
}
