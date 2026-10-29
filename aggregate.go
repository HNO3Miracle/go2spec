package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/version"
	"io/fs"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

type aggregateRequest struct {
	Root    string
	Name    string
	Version string
	License string
	URL     string
	Modules []module.Version
}

type aggregatePlan struct {
	Request   aggregateRequest
	Sources   []moduleSource
	License   string
	GoVersion string
	BuildDeps []string
	RunDeps   []string
}

var (
	rpmNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.+_-]*$`)
	rpmVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~^]*$`)
	importRootPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+$`)
	licenseIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+:-]*$`)
	goVersionPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)
)

var pkgsiteModuleTimeout = 30 * time.Second

func parseAggregateRequest(root, name, rpmVersion, license string, args []string) (aggregateRequest, error) {
	req := aggregateRequest{Root: root, Name: name, Version: rpmVersion, License: license}
	// This is an import prefix, not necessarily a module (or even a package).
	if err := module.CheckImportPath(root); err != nil || !importRootPattern.MatchString(root) {
		return req, fmt.Errorf("-combine requires a safe common Go import root")
	}
	if !rpmNamePattern.MatchString(name) {
		return req, fmt.Errorf("-package_name requires a literal RPM package name")
	}
	if !rpmVersionPattern.MatchString(rpmVersion) {
		return req, fmt.Errorf("-package_version requires an RPM Version without Epoch or Release")
	}
	if license != "" && !validLicenseExpression(license) {
		return req, fmt.Errorf("-license requires a literal license expression")
	}
	if len(args) == 0 {
		return req, fmt.Errorf("-combine requires explicit module@version arguments")
	}
	seen := make(map[string]bool)
	for _, arg := range args {
		p, v, err := parsePackArg(arg, "")
		if err != nil {
			return req, err
		}
		if v == "" || module.CanonicalVersion(v) != v {
			return req, fmt.Errorf("%q: specify a canonical Go version, not latest, a branch or an unversioned revision", arg)
		}
		if err := module.Check(p, v); err != nil {
			return req, err
		}
		if !importRootPattern.MatchString(p) || !importWithin(p, root) {
			return req, fmt.Errorf("%q: module is outside -combine root %q or has an unsupported path", p, root)
		}
		if seen[p] {
			return req, fmt.Errorf("%q: duplicate module; one import path cannot contain two versions", p)
		}
		seen[p] = true
		req.Modules = append(req.Modules, module.Version{Path: p, Version: v})
	}
	slices.SortFunc(req.Modules, func(a, b module.Version) int { return strings.Compare(a.Path, b.Path) })
	return req, nil
}

func importWithin(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

func compareGoVersions(a, b string) int {
	return version.Compare("go"+a, "go"+b)
}

func validLicenseExpression(text string) bool {
	tokens := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(text))
	depth, operand := 0, true
	canWith, exception := false, false
	for _, token := range tokens {
		switch token {
		case "(":
			if !operand || exception {
				return false
			}
			depth++
		case ")":
			if operand || depth == 0 {
				return false
			}
			depth--
			canWith = false
		case "AND", "OR":
			if operand {
				return false
			}
			operand, canWith = true, false
		case "WITH":
			if operand || !canWith {
				return false
			}
			operand, exception, canWith = true, true, false
		default:
			if !operand || !licenseIDPattern.MatchString(token) || token == "TODO" || token == "UNKNOWN" {
				return false
			}
			operand, canWith, exception = false, !exception, false
		}
	}
	return depth == 0 && !operand && !strings.ContainsAny(text, "\r\n\x00")
}

func sourceLicenseFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToUpper(entry.Name())
		recognized := false
		for _, base := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE"} {
			if name == base || strings.HasPrefix(name, base+".") || strings.HasPrefix(name, base+"-") || strings.HasPrefix(name, base+"_") {
				recognized = true
				break
			}
		}
		if !recognized {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	slices.Sort(files)
	return files, err
}

func moduleLicense(ctx context.Context, source *moduleSource, explicit string) (string, error) {
	files, err := sourceLicenseFiles(source.Dir)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%s: source has no recognized license file; additional source material requires manual packaging", source.Module.Path)
	}
	source.LicenseFiles = files
	if explicit != "" {
		return explicit, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, pkgsiteModuleTimeout)
	defer cancel()
	metadata, err := getPkgsiteModule(lookupCtx, source.Module.Path, source.Module.Version)
	if err != nil {
		return "", fmt.Errorf("%s: cannot obtain versioned license metadata; supply a reviewed -license expression", source.Module.Path)
	}
	if metadata.Path != source.Module.Path || metadata.Version != source.Module.Version || !metadata.IsRedistributable {
		return "", fmt.Errorf("%s: license metadata identity or redistribution status requires manual review", source.Module.Path)
	}
	// Every detected license must be represented by metadata for this exact source.
	var licenses []pkgsiteLicense
	for _, file := range files {
		found := false
		for _, license := range metadata.Licenses {
			if license.FilePath != file || len(license.Types) == 0 || license.Contents == "" {
				continue
			}
			contents, err := os.ReadFile(filepath.Join(source.Dir, filepath.FromSlash(file)))
			if err != nil {
				return "", err
			}
			if strings.ReplaceAll(string(contents), "\r\n", "\n") != strings.ReplaceAll(license.Contents, "\r\n", "\n") {
				return "", fmt.Errorf("%s: license metadata does not match Source; supply a reviewed -license expression", source.Module.Path)
			}
			// The old helper prefers root files; each file here must contribute.
			licenses = append(licenses, pkgsiteLicense{FilePath: path.Base(file), Types: license.Types})
			found = true
			break
		}
		if !found {
			return "", fmt.Errorf("%s: incomplete license metadata; supply a reviewed -license expression", source.Module.Path)
		}
	}
	result := pkgsiteLicenseExpression(licenses)
	if !validLicenseExpression(result) {
		return "", fmt.Errorf("%s: unsupported license metadata", source.Module.Path)
	}
	return result, nil
}

func aggregateStagingPrefix(name string) string {
	return ".go2spec-combine-" + name + "-"
}

const (
	aggregateIncompleteMarkerName = ".go2spec-incomplete"
	aggregateStagingLockName      = ".go2spec-lock"
)

func aggregateStagingMarker(staging string) []byte {
	return []byte("go2spec aggregate staging v1\n" + filepath.Base(staging) + "\n")
}

func recoverAggregateStagingTree(staging string) error {
	lock, locked, err := tryLockAggregateStaging(filepath.Join(staging, aggregateStagingLockName))
	if err != nil || !locked {
		return err
	}
	defer releaseAggregateStagingLock(lock)
	marker := filepath.Join(staging, aggregateIncompleteMarkerName)
	data, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !bytes.Equal(data, aggregateStagingMarker(staging)) {
		return fmt.Errorf("refusing to remove unrecognized staging directory %q", staging)
	}
	if err := removeAggregateWork(staging); err != nil {
		return fmt.Errorf("recover incomplete output %q: %w", staging, err)
	}
	return nil
}

func recoverAggregateStaging(parent, name string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	prefix := aggregateStagingPrefix(name)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || !entry.IsDir() {
			continue
		}
		if err := recoverAggregateStagingTree(filepath.Join(parent, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func prepareAggregate(ctx context.Context, req aggregateRequest, parent string) (string, error) {
	final := filepath.Join(parent, req.Name)
	if _, err := os.Lstat(final); err == nil {
		return "", fmt.Errorf("output %q already exists; refusing to replace it", final)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := recoverAggregateStaging(parent, req.Name); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(parent, aggregateStagingPrefix(req.Name))
	if err != nil {
		return "", err
	}
	lock, err := acquireAggregateStagingLock(filepath.Join(work, aggregateStagingLockName))
	if err != nil {
		_ = removeAggregateWork(work)
		return "", err
	}
	defer releaseAggregateStagingLock(lock)
	marker := filepath.Join(work, aggregateIncompleteMarkerName)
	markerTemp := marker + ".tmp"
	if err := os.WriteFile(markerTemp, aggregateStagingMarker(work), 0600); err != nil {
		_ = removeAggregateWork(work)
		return "", err
	}
	if err := os.Rename(markerTemp, marker); err != nil {
		_ = removeAggregateWork(work)
		return "", err
	}
	defer func() {
		if err := removeAggregateWork(work); err != nil {
			log.Printf("combine: cannot clean temporary directory %s: %v", work, err)
		}
	}()
	// An output parent may itself be in a Go module. Do not inherit its
	// go.mod, replacements or toolchain requirement while resolving sources.
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module go2spec.invalid/combine\n\ngo 1.16\n"), 0644); err != nil {
		return "", err
	}
	plan, err := buildAggregatePlan(ctx, req, work)
	if err != nil {
		return "", err
	}
	output := filepath.Join(work, "output")
	if err := os.Mkdir(output, 0755); err != nil {
		return "", err
	}
	if err := writeAggregateSpec(output, plan); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Publish the complete output directory atomically. A marked staging tree
	// left by interruption can be recovered by the next invocation.
	if err := publishAggregateDirectory(output, final); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("output %q already exists; refusing to replace it", final)
		}
		return "", fmt.Errorf("cannot publish output %q: %w", final, err)
	}
	return final, nil
}

func removeAggregateWork(work string) error {
	// Go's private module cache contains read-only directories. Only this
	// invocation's temporary tree is made writable, never the user's Go cache.
	if err := filepath.WalkDir(work, func(p string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			return os.Chmod(p, 0700)
		}
		return err
	}); err != nil {
		return err
	}
	return os.RemoveAll(work)
}

func effectivePrivatePatterns(ctx context.Context, work string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOPRIVATE", "GONOPROXY", "GONOSUMDB")
	cmd.Dir, cmd.Env = work, combinedGoEnv(work)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cannot determine Go private-module configuration")
	}
	patterns := strings.Fields(string(out))
	patterns = append(patterns, os.Getenv("GOPRIVATE"), os.Getenv("GONOPROXY"), os.Getenv("GONOSUMDB"))
	return patterns, nil
}

func validateProjectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return fmt.Errorf("-url requires an HTTPS project URL without credentials, query, fragment or control characters")
	}
	return nil
}

func selectedModuleRequirementError(plan aggregatePlan) error {
	selected := make(map[string]string, len(plan.Sources))
	for _, source := range plan.Sources {
		selected[source.Module.Path] = source.Module.Version
	}
	for _, source := range plan.Sources {
		for _, required := range source.Requires {
			chosen, ok := selected[required.Path]
			if !ok || semver.Compare(required.Version, chosen) <= 0 {
				continue
			}
			return fmt.Errorf("%s requires %s >= %s, but combine selected %s; choose a compatible module version", source.Module.Path, required.Path, required.Version, chosen)
		}
	}
	return nil
}

func buildAggregatePlan(ctx context.Context, req aggregateRequest, work string) (aggregatePlan, error) {
	plan := aggregatePlan{Request: req, GoVersion: "1.16"}
	if req.License == "" {
		patterns, err := effectivePrivatePatterns(ctx, work)
		if err != nil {
			return plan, err
		}
		for _, mod := range req.Modules {
			for _, pattern := range patterns {
				if pattern != "" && module.MatchPrefixPatterns(pattern, mod.Path) {
					return plan, fmt.Errorf("%s: Go private-module configuration (GOPRIVATE/GONOPROXY/GONOSUMDB) requires a reviewed -license expression; no public metadata lookup performed", mod.Path)
				}
			}
		}
	}
	date := packagingDateString()
	licenseSet := make(map[string]bool)
	for i, mod := range req.Modules {
		source, err := downloadModuleSource(ctx, work, mod, date, i)
		if err != nil {
			return plan, err
		}
		if !goVersionPattern.MatchString(source.GoVersion) {
			return plan, fmt.Errorf("%s: unsupported minimum Go toolchain %q", mod.Path, source.GoVersion)
		}
		if compareGoVersions(source.GoVersion, plan.GoVersion) > 0 {
			plan.GoVersion = source.GoVersion
		}
		license, err := moduleLicense(ctx, &source, req.License)
		if err != nil {
			return plan, err
		}
		licenseSet[license] = true
		plan.Sources = append(plan.Sources, source)
	}
	if err := selectedModuleRequirementError(plan); err != nil {
		return plan, err
	}
	licenses := sortedDependencySet(licenseSet)
	for i, license := range licenses {
		if len(licenses) > 1 && strings.Contains(license, " ") {
			licenses[i] = "(" + license + ")"
		}
	}
	plan.License = strings.Join(licenses, " AND ")
	if err := validateAggregateLayout(plan); err != nil {
		return plan, err
	}
	if err := aggregateDependencies(ctx, work, &plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func validateAggregateLayout(plan aggregatePlan) error {
	for i, source := range plan.Sources {
		err := filepath.WalkDir(source.Dir, func(p string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(source.Dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if path.Base(rel) == "go.mod" && rel != "go.mod" {
				return fmt.Errorf("%s: unexpected nested go.mod %s; missing independent module mapping", source.Module.Path, rel)
			}
			installed := source.Module.Path + "/" + rel
			for j, other := range plan.Sources {
				if i != j && importWithin(other.Module.Path, source.Module.Path) &&
					(importWithin(installed, other.Module.Path) || importWithin(other.Module.Path, installed)) {
					return fmt.Errorf("%s: Source file %s overlaps selected module %s", source.Module.Path, rel, other.Module.Path)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
