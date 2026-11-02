package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/module"
)

type repositoryDependency struct {
	Capability  string
	Scope       string
	Requirement *rpmEVRRequirement
	GoModule    *module.Version
	InputError  string
}

type dependencyDiagnostic struct {
	Capability         string             `json:"capability"`
	Scope              string             `json:"scope"`
	Status             string             `json:"status"`
	Reason             string             `json:"reason,omitempty"`
	VersionBasis       string             `json:"version_basis,omitempty"`
	ExpectedRPMVersion string             `json:"expected_rpm_version,omitempty"`
	Requirement        *rpmEVRRequirement `json:"rpm_requirement,omitempty"`
	GoModule           *module.Version    `json:"go_module,omitempty"`
	Providers          []rpmProvider      `json:"providers,omitempty"`
}

type repositoryQueryReport struct {
	Schema       int                    `json:"schema"`
	Snapshot     repositorySnapshot     `json:"snapshot"`
	Dependencies []dependencyDiagnostic `json:"dependencies"`
	Errors       []string               `json:"errors,omitempty"`
}

var (
	repositoryCapabilityPattern  = regexp.MustCompile(`^go\([^()\r\n]+\)$`)
	repositoryRequirementPattern = regexp.MustCompile(`^(go\([^()\r\n]+\))(?:\s*(<=|>=|=|<|>)\s*([^\s]+))?$`)
)

func parseRepositoryCapability(raw string) (string, error) {
	capability := strings.TrimSpace(raw)
	if !repositoryCapabilityPattern.MatchString(capability) {
		return "", fmt.Errorf("invalid Go RPM capability %q", raw)
	}
	importPath := strings.TrimSuffix(strings.TrimPrefix(capability, "go("), ")")
	if err := module.CheckImportPath(importPath); err != nil {
		return "", fmt.Errorf("invalid Go RPM capability %q: %w", raw, err)
	}
	return capability, nil
}

func parseRepositoryRPMRequirement(raw string) (repositoryDependency, error) {
	match := repositoryRequirementPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return repositoryDependency{}, fmt.Errorf("invalid repository requirement %q; expected go(path)[operator]epoch:version-release", raw)
	}
	capability, err := parseRepositoryCapability(match[1])
	if err != nil {
		return repositoryDependency{}, err
	}
	dependency := repositoryDependency{Capability: capability, Scope: "explicit"}
	if match[2] == "" {
		return dependency, nil
	}
	evr, err := parseRPMEVR(match[3])
	if err != nil {
		return repositoryDependency{}, fmt.Errorf("%s: %w", raw, err)
	}
	dependency.Requirement = &rpmEVRRequirement{Operator: match[2], EVR: evr}
	return dependency, nil
}

func parseRPMEVR(raw string) (rpmEVR, error) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, "\r\n\x00%{}$\\/\t ") {
		return rpmEVR{}, fmt.Errorf("RPM EVR is empty or contains control characters")
	}
	evr := rpmEVR{Epoch: "0"}
	if colon := strings.IndexByte(value, ':'); colon >= 0 {
		evr.Epoch = value[:colon]
		value = value[colon+1:]
	}
	if dash := strings.LastIndexByte(value, '-'); dash > 0 {
		evr.Version, evr.Release = value[:dash], value[dash+1:]
	} else {
		evr.Version = value
	}
	if evr.Version == "" || strings.ContainsAny(evr.Epoch, "^~") || strings.ContainsAny(evr.Version, "\r\n\x00") || strings.ContainsAny(evr.Release, "\r\n\x00") {
		return rpmEVR{}, fmt.Errorf("invalid RPM EVR %q", raw)
	}
	return evr, nil
}

func readRepositorySpec(path string) ([]repositoryDependency, []string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var dependencies []repositoryDependency
	var errorsFound []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "BuildRequires:") && !strings.HasPrefix(line, "Requires:") {
			continue
		}
		scope := "runtime"
		value := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		if strings.HasPrefix(line, "BuildRequires:") {
			scope = "build"
		}
		if !strings.Contains(value, "go(") {
			continue
		}
		dependency, err := parseRepositoryRPMRequirement(value)
		if err != nil {
			dependencies = append(dependencies, repositoryDependency{Capability: value, Scope: scope, InputError: err.Error()})
			continue
		}
		dependency.Scope = scope
		dependencies = append(dependencies, dependency)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return dependencies, errorsFound, nil
}

func parseRepositoryModuleRequirement(raw string) (module.Version, error) {
	path, version, ok := strings.Cut(strings.TrimSpace(raw), "@")
	if !ok || path == "" || version == "" || module.CanonicalVersion(version) != version {
		return module.Version{}, fmt.Errorf("module requirement %q must be module@canonical-version", raw)
	}
	if err := module.Check(path, version); err != nil {
		return module.Version{}, err
	}
	return module.Version{Path: path, Version: version}, nil
}

func attachRepositoryModuleRequirements(dependencies []repositoryDependency, modules []module.Version) {
	slices.SortFunc(modules, func(a, b module.Version) int {
		return len(b.Path) - len(a.Path)
	})
	for i := range dependencies {
		for _, required := range modules {
			path := strings.TrimSuffix(strings.TrimPrefix(dependencies[i].Capability, "go("), ")")
			if path == required.Path || strings.HasPrefix(path, required.Path+"/") {
				copy := required
				dependencies[i].GoModule = &copy
				break
			}
		}
	}
}

func diagnoseRepositoryDependencies(ctx context.Context, catalog repositoryCatalog, dependencies []repositoryDependency, compare rpmEVRComparator) repositoryQueryReport {
	report := repositoryQueryReport{Schema: 1, Snapshot: catalog.Snapshot}
	seen := make(map[string]bool)
	for _, dependency := range dependencies {
		key := dependency.Scope + "\x00" + dependency.Capability
		if dependency.Requirement != nil {
			key += "\x00" + dependency.Requirement.Operator + "\x00" + formatRPMEVR(dependency.Requirement.EVR)
		}
		if dependency.GoModule != nil {
			key += "\x00" + dependency.GoModule.Path + "@" + dependency.GoModule.Version
		}
		if dependency.InputError != "" {
			key += "\x00" + dependency.InputError
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		providers := append([]rpmProvider(nil), catalog.Providers[dependency.Capability]...)
		diagnostic := dependencyDiagnostic{
			Capability:  dependency.Capability,
			Scope:       dependency.Scope,
			Requirement: dependency.Requirement,
			GoModule:    dependency.GoModule,
			Providers:   providers,
		}
		if dependency.InputError != "" {
			diagnostic.Status = "unknown"
			diagnostic.Reason = dependency.InputError
		} else {
			diagnostic.Status, diagnostic.Reason = diagnoseRepositoryProvider(ctx, providers, dependency.Requirement, compare)
		}
		if dependency.Requirement == nil && dependency.GoModule != nil {
			diagnostic.Status, diagnostic.Reason, diagnostic.VersionBasis, diagnostic.ExpectedRPMVersion = diagnoseGoModuleRequirement(ctx, providers, *dependency.GoModule)
		}
		report.Dependencies = append(report.Dependencies, diagnostic)
	}
	slices.SortFunc(report.Dependencies, func(a, b dependencyDiagnostic) int {
		if a.Capability != b.Capability {
			return strings.Compare(a.Capability, b.Capability)
		}
		return strings.Compare(a.Scope, b.Scope)
	})
	return report
}

func diagnoseGoModuleRequirement(_ context.Context, providers []rpmProvider, required module.Version) (string, string, string, string) {
	basis := "go-module-version-not-comparable-to-rpm-evr"
	if len(providers) == 0 {
		return "missing", "no exact provider exists; Go Module minimum was not used to infer an RPM version", basis, ""
	}
	if len(providers) > 1 {
		return "multiple-providers", "multiple exact providers exist; Go Module minimum cannot select an RPM provider", basis, ""
	}
	return "unknown", fmt.Sprintf("provider exists for %s@%s, but Go Module and RPM EVR versions are not automatically comparable", required.Path, required.Version), basis, ""
}
func diagnoseRepositoryProvider(ctx context.Context, providers []rpmProvider, requirement *rpmEVRRequirement, compare rpmEVRComparator) (string, string) {
	if len(providers) == 0 {
		return "missing", "no exact go(...) provider exists for the selected repository and architecture"
	}
	if requirement == nil {
		if len(providers) > 1 {
			return "multiple-providers", "multiple exact providers exist; go2spec does not choose one"
		}
		return "present-no-version-requirement", "an exact provider exists, but no RPM EVR requirement was supplied"
	}
	matched, unknown := 0, false
	for _, provider := range providers {
		satisfied, comparable, err := rpmRequirementSatisfied(ctx, provider, requirement, compare)
		if err != nil {
			unknown = true
			continue
		}
		if !comparable {
			unknown = true
			continue
		}
		if satisfied {
			matched++
		}
	}
	if matched > 0 {
		if len(providers) > 1 {
			return "multiple-providers", "more than one exact provider is available; selection remains ambiguous"
		}
		return "satisfied", "the exact provider satisfies the explicit RPM EVR requirement"
	}
	if unknown {
		return "unknown", "at least one provider has no comparable RPM EVR or the RPM comparator is unavailable"
	}
	return "version-insufficient", "providers exist, but none satisfies the explicit RPM EVR requirement"
}

func reportJSON(report repositoryQueryReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func renderRepositoryReport(report repositoryQueryReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s (%s)\n", report.Snapshot.Repository, report.Snapshot.Distro)
	fmt.Fprintf(&b, "Base URL: %s\nArchitecture: %s\n", report.Snapshot.BaseURL, report.Snapshot.Arch)
	fmt.Fprintf(&b, "repomd revision=%s sha256=%s\n", report.Snapshot.Revision, report.Snapshot.RepomdSHA256)
	fmt.Fprintf(&b, "primary sha256=%s open-sha256=%s signature=%s\n", report.Snapshot.PrimaryChecksum, report.Snapshot.PrimaryOpenChecksum, report.Snapshot.SignatureStatus)
	for _, dependency := range report.Dependencies {
		fmt.Fprintf(&b, "\n[%s] %s: %s\n", dependency.Status, dependency.Scope, dependency.Capability)
		if dependency.Reason != "" {
			fmt.Fprintf(&b, "  reason: %s\n", dependency.Reason)
		}
		if dependency.GoModule != nil {
			fmt.Fprintf(&b, "  Go module: %s@%s (not an RPM EVR comparison)\n", dependency.GoModule.Path, dependency.GoModule.Version)
		}
		for _, provider := range dependency.Providers {
			provideEVR := "unversioned"
			if provider.ProvideEVR != nil {
				provideEVR = formatRPMEVR(*provider.ProvideEVR)
			}
			fmt.Fprintf(&b, "  provider: %s.%s EVR=%s provide=%s %s\n", provider.Package, provider.Arch, formatRPMEVR(provider.PackageEVR), provideEVR, provider.Location)
		}
	}
	return b.String()
}

func formatRPMEVR(evr rpmEVR) string {
	value := evr.Version
	if evr.Epoch != "" && evr.Epoch != "0" {
		value = evr.Epoch + ":" + value
	}
	if evr.Release != "" {
		value += "-" + evr.Release
	}
	return value
}
