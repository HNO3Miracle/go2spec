package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
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

type repositoryModuleVersion struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type providerVersionCheck struct {
	Provider rpmProvider `json:"provider"`
	Status   string      `json:"status"`
	Reason   string      `json:"reason,omitempty"`
}

type dependencyDiagnostic struct {
	Capability         string                   `json:"capability"`
	Scope              string                   `json:"scope"`
	Status             string                   `json:"status"`
	Reason             string                   `json:"reason,omitempty"`
	VersionBasis       string                   `json:"version_basis,omitempty"`
	ExpectedRPMVersion string                   `json:"expected_rpm_version,omitempty"`
	Requirement        *rpmEVRRequirement       `json:"rpm_requirement,omitempty"`
	GoModule           *repositoryModuleVersion `json:"go_module,omitempty"`
	Providers          []rpmProvider            `json:"providers,omitempty"`
	ProviderChecks     []providerVersionCheck   `json:"provider_checks,omitempty"`
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
	rpmEpochPattern              = regexp.MustCompile(`^[0-9]+$`)
	rpmLabelPattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~^]*$`)
	repositorySpecDependency     = regexp.MustCompile(`^(BuildRequires|Requires)(?:\([^)]*\))?:\s*(.*)$`)
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
		if colon == 0 || strings.IndexByte(value[colon+1:], ':') >= 0 {
			return rpmEVR{}, fmt.Errorf("invalid RPM EVR %q: invalid epoch separator", raw)
		}
		evr.Epoch = value[:colon]
		value = value[colon+1:]
	}
	if dash := strings.LastIndexByte(value, '-'); dash >= 0 {
		evr.Version, evr.Release = value[:dash], value[dash+1:]
		if evr.Release == "" {
			return rpmEVR{}, fmt.Errorf("invalid RPM EVR %q: release must not be empty", raw)
		}
	} else {
		evr.Version = value
	}
	if err := validateRPMEVR(evr); err != nil {
		return rpmEVR{}, fmt.Errorf("invalid RPM EVR %q: %w", raw, err)
	}
	return evr, nil
}

func validateRPMEVR(evr rpmEVR) error {
	epoch := normalizeEpoch(evr.Epoch)
	if !rpmEpochPattern.MatchString(epoch) {
		return fmt.Errorf("epoch %q must contain only decimal digits", epoch)
	}
	if _, err := strconv.ParseUint(epoch, 10, 64); err != nil {
		return fmt.Errorf("invalid epoch %q: %w", epoch, err)
	}
	if !rpmLabelPattern.MatchString(evr.Version) {
		return fmt.Errorf("version %q contains characters outside the supported RPM label syntax", evr.Version)
	}
	if evr.Release != "" && !rpmLabelPattern.MatchString(evr.Release) {
		return fmt.Errorf("release %q contains characters outside the supported RPM label syntax", evr.Release)
	}
	return nil
}

type specCondition int

const (
	specConditionFalse specCondition = iota
	specConditionTrue
	specConditionUnknown
)

type specConditionalScope struct {
	parent   specCondition
	branch   specCondition
	elseSeen bool
}

func combineSpecCondition(parent, branch specCondition) specCondition {
	if parent == specConditionFalse || branch == specConditionFalse {
		return specConditionFalse
	}
	if parent == specConditionUnknown || branch == specConditionUnknown {
		return specConditionUnknown
	}
	return specConditionTrue
}

func evaluateSpecArchCondition(args []string, targetArch string, negate bool) specCondition {
	if targetArch == "" || len(args) == 0 {
		return specConditionUnknown
	}
	matched := false
	for _, list := range strings.Split(strings.Join(args, " "), ",") {
		architectures := strings.Fields(list)
		if len(architectures) == 0 {
			return specConditionUnknown
		}
		for _, arch := range architectures {
			if strings.ContainsAny(arch, "%{}()[]") {
				return specConditionUnknown
			}
			if arch == targetArch {
				matched = true
			}
		}
	}
	if negate {
		matched = !matched
	}
	if matched {
		return specConditionTrue
	}
	return specConditionFalse
}

func containsRPMMacro(value string) bool {
	return strings.Contains(value, "%{") || strings.Contains(value, "%(") || strings.Contains(value, "%[")
}

func readRepositorySpec(path, targetArch string) ([]repositoryDependency, []string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var dependencies []repositoryDependency
	var errorsFound []string
	var scopes []specConditionalScope
	active := specConditionTrue
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directive := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "%ifarch", "%ifnarch", "%if", "%ifos", "%ifnos":
			branch := specConditionUnknown
			if fields[0] == "%ifarch" || fields[0] == "%ifnarch" {
				branch = evaluateSpecArchCondition(fields[1:], targetArch, fields[0] == "%ifnarch")
			}
			scopes = append(scopes, specConditionalScope{parent: active, branch: branch})
			active = combineSpecCondition(active, branch)
			continue
		case "%elif", "%elifarch", "%elifnarch":
			if len(scopes) == 0 || scopes[len(scopes)-1].elseSeen {
				return nil, nil, fmt.Errorf("line %d: misplaced %s", lineNumber, fields[0])
			}
			scope := &scopes[len(scopes)-1]
			scope.branch = specConditionUnknown
			active = combineSpecCondition(scope.parent, scope.branch)
			continue
		case "%else":
			if len(scopes) == 0 || scopes[len(scopes)-1].elseSeen {
				return nil, nil, fmt.Errorf("line %d: misplaced %%else", lineNumber)
			}
			scope := &scopes[len(scopes)-1]
			scope.elseSeen = true
			if scope.branch == specConditionTrue {
				scope.branch = specConditionFalse
			} else if scope.branch == specConditionFalse {
				scope.branch = specConditionTrue
			}
			active = combineSpecCondition(scope.parent, scope.branch)
			continue
		case "%endif":
			if len(scopes) == 0 {
				return nil, nil, fmt.Errorf("line %d: unmatched %%endif", lineNumber)
			}
			active = scopes[len(scopes)-1].parent
			scopes = scopes[:len(scopes)-1]
			continue
		}

		match := repositorySpecDependency.FindStringSubmatch(directive)
		if match == nil || active == specConditionFalse {
			continue
		}
		scope, value := "runtime", strings.TrimSpace(match[2])
		if match[1] == "BuildRequires" {
			scope = "build"
		}
		macro := containsRPMMacro(value)
		if !strings.Contains(value, "go(") && !macro {
			continue
		}
		var inputError string
		if active == specConditionUnknown {
			inputError = fmt.Sprintf("line %d: dependency is inside a conditional that cannot be evaluated for target architecture %q", lineNumber, targetArch)
		} else if macro {
			inputError = fmt.Sprintf("line %d: macro-expanded RPM dependency cannot be resolved safely", lineNumber)
		}
		if inputError != "" {
			dependencies = append(dependencies, repositoryDependency{Capability: value, Scope: scope, InputError: inputError})
			errorsFound = append(errorsFound, inputError)
			continue
		}
		dependency, err := parseRepositoryRPMRequirement(value)
		if err != nil {
			inputError = fmt.Sprintf("line %d: %v", lineNumber, err)
			dependencies = append(dependencies, repositoryDependency{Capability: value, Scope: scope, InputError: inputError})
			errorsFound = append(errorsFound, inputError)
			continue
		}
		dependency.Scope = scope
		dependencies = append(dependencies, dependency)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if len(scopes) != 0 {
		return nil, nil, fmt.Errorf("spec ends with an unterminated conditional")
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

func attachRepositoryModuleRequirements(dependencies []repositoryDependency, modules []module.Version) []string {
	sortedModules := append([]module.Version(nil), modules...)
	slices.SortFunc(sortedModules, func(a, b module.Version) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	conflictsByPath := make(map[string]string)
	var conflicts []string
	for i := 0; i < len(sortedModules); {
		j := i + 1
		versions := []string{sortedModules[i].Version}
		for j < len(sortedModules) && sortedModules[j].Path == sortedModules[i].Path {
			if sortedModules[j].Version != versions[len(versions)-1] {
				versions = append(versions, sortedModules[j].Version)
			}
			j++
		}
		if len(versions) > 1 {
			message := fmt.Sprintf("conflicting --module-require versions for %s: %s; no version was selected", sortedModules[i].Path, strings.Join(versions, ", "))
			conflictsByPath[sortedModules[i].Path] = message
			conflicts = append(conflicts, message)
		}
		i = j
	}
	matchedModules := make(map[module.Version]bool)
	for i := range dependencies {
		importPath := strings.TrimSuffix(strings.TrimPrefix(dependencies[i].Capability, "go("), ")")
		longest := 0
		var selected *module.Version
		for j := range sortedModules {
			required := &sortedModules[j]
			if importPath != required.Path && !strings.HasPrefix(importPath, required.Path+"/") {
				continue
			}
			if len(required.Path) > longest {
				longest = len(required.Path)
				selected = required
			}
		}
		if selected == nil {
			continue
		}
		if message := conflictsByPath[selected.Path]; message != "" {
			dependencies[i].GoModule = nil
			if dependencies[i].InputError == "" {
				dependencies[i].InputError = message
			} else if !strings.Contains(dependencies[i].InputError, message) {
				dependencies[i].InputError += "; " + message
			}
			continue
		}
		matchedModules[*selected] = true
		copy := *selected
		dependencies[i].GoModule = &copy
	}
	for _, required := range sortedModules {
		if matchedModules[required] {
			continue
		}
		if conflict := conflictsByPath[required.Path]; conflict != "" {
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("--module-require %s@%s does not match any queried Go package capability", required.Path, required.Version))
	}
	slices.Sort(conflicts)
	return slices.Compact(conflicts)
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
		slices.SortFunc(providers, compareRPMProvider)
		diagnostic := dependencyDiagnostic{
			Capability:  dependency.Capability,
			Scope:       dependency.Scope,
			Requirement: dependency.Requirement,
			Providers:   providers,
		}
		if dependency.GoModule != nil {
			diagnostic.GoModule = &repositoryModuleVersion{Path: dependency.GoModule.Path, Version: dependency.GoModule.Version}
		}
		if dependency.InputError != "" {
			diagnostic.Status = "unknown"
			diagnostic.Reason = dependency.InputError
		} else {
			diagnostic.Status, diagnostic.Reason, diagnostic.ProviderChecks = diagnoseRepositoryProviderDetails(ctx, providers, dependency.Requirement, compare)
		}
		if dependency.InputError == "" && dependency.Requirement == nil && dependency.GoModule != nil {
			diagnostic.Status, diagnostic.Reason, diagnostic.VersionBasis, diagnostic.ExpectedRPMVersion = diagnoseGoModuleRequirement(ctx, providers, *dependency.GoModule)
		}
		report.Dependencies = append(report.Dependencies, diagnostic)
	}
	slices.SortFunc(report.Dependencies, compareDependencyDiagnostics)
	return report
}

func compareDependencyDiagnostics(a, b dependencyDiagnostic) int {
	if c := strings.Compare(a.Capability, b.Capability); c != 0 {
		return c
	}
	if c := strings.Compare(a.Scope, b.Scope); c != 0 {
		return c
	}
	if c := compareRPMRequirements(a.Requirement, b.Requirement); c != 0 {
		return c
	}
	if c := compareRepositoryModules(a.GoModule, b.GoModule); c != 0 {
		return c
	}
	if c := strings.Compare(a.Status, b.Status); c != 0 {
		return c
	}
	return strings.Compare(a.Reason, b.Reason)
}

func compareRPMRequirements(a, b *rpmEVRRequirement) int {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return 0
		}
		if a == nil {
			return -1
		}
		return 1
	}
	if c := strings.Compare(a.Operator, b.Operator); c != 0 {
		return c
	}
	if c := strings.Compare(normalizeEpoch(a.EVR.Epoch), normalizeEpoch(b.EVR.Epoch)); c != 0 {
		return c
	}
	if c := strings.Compare(a.EVR.Version, b.EVR.Version); c != 0 {
		return c
	}
	return strings.Compare(a.EVR.Release, b.EVR.Release)
}

func compareRepositoryModules(a, b *repositoryModuleVersion) int {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return 0
		}
		if a == nil {
			return -1
		}
		return 1
	}
	if c := strings.Compare(a.Path, b.Path); c != 0 {
		return c
	}
	return strings.Compare(a.Version, b.Version)
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
	status, reason, _ := diagnoseRepositoryProviderDetails(ctx, providers, requirement, compare)
	return status, reason
}

func diagnoseRepositoryProviderDetails(ctx context.Context, providers []rpmProvider, requirement *rpmEVRRequirement, compare rpmEVRComparator) (string, string, []providerVersionCheck) {
	if len(providers) == 0 {
		return "missing", "no exact go(...) provider exists for the selected repository and architecture", nil
	}
	if requirement == nil {
		if len(providers) > 1 {
			return "multiple-providers", "multiple exact providers exist; go2spec does not choose one", nil
		}
		return "present-no-version-requirement", "an exact provider exists, but no RPM EVR requirement was supplied", nil
	}
	checks := make([]providerVersionCheck, 0, len(providers))
	for _, provider := range providers {
		satisfied, comparable, err := rpmRequirementSatisfied(ctx, provider, requirement, compare)
		check := providerVersionCheck{Provider: provider}
		switch {
		case err != nil:
			check.Status = "unknown"
			check.Reason = err.Error()
		case !comparable:
			check.Status = "unknown"
			check.Reason = "provider does not expose a comparable versioned EQ Provide"
		case satisfied:
			check.Status = "satisfied"
			check.Reason = "provider satisfies the explicit RPM EVR requirement"
		default:
			check.Status = "version-insufficient"
			check.Reason = "provider does not satisfy the explicit RPM EVR requirement"
		}
		checks = append(checks, check)
	}
	if len(providers) > 1 {
		return "multiple-providers", "multiple exact providers exist; provider selection remains ambiguous; see per-provider checks", checks
	}
	check := checks[0]
	return check.Status, check.Reason, checks
}

func reportJSON(report repositoryQueryReport) ([]byte, error) {
	slices.SortFunc(report.Dependencies, compareDependencyDiagnostics)
	slices.Sort(report.Errors)
	report.Errors = slices.Compact(report.Errors)
	for i := range report.Dependencies {
		diagnostic := &report.Dependencies[i]
		slices.SortFunc(diagnostic.Providers, compareRPMProvider)
		slices.SortFunc(diagnostic.ProviderChecks, func(a, b providerVersionCheck) int {
			return compareRPMProvider(a.Provider, b.Provider)
		})
	}
	return json.MarshalIndent(report, "", "  ")
}

func renderRepositoryReport(report repositoryQueryReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s (%s)\n", report.Snapshot.Repository, report.Snapshot.Distro)
	fmt.Fprintf(&b, "Base URL: %s\nArchitecture: %s\n", report.Snapshot.BaseURL, report.Snapshot.Arch)
	fmt.Fprintf(&b, "repomd revision=%s sha256=%s\n", report.Snapshot.Revision, report.Snapshot.RepomdSHA256)
	openChecksum := "not-provided"
	if report.Snapshot.PrimaryOpenChecksumType != "" {
		openChecksum = report.Snapshot.PrimaryOpenChecksumType + ":" + report.Snapshot.PrimaryOpenChecksum
	}
	openSize := "not-provided"
	if report.Snapshot.PrimaryOpenSize > 0 {
		openSize = fmt.Sprintf("%d bytes", report.Snapshot.PrimaryOpenSize)
	}
	fmt.Fprintf(&b, "primary checksum=%s:%s (%d bytes); open checksum=%s (%s, %s); signature=%s\n",
		report.Snapshot.PrimaryChecksumType, report.Snapshot.PrimaryChecksum, report.Snapshot.PrimarySize,
		openChecksum, openSize, report.Snapshot.PrimaryOpenChecksumStatus, report.Snapshot.SignatureStatus)
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
		for _, check := range dependency.ProviderChecks {
			fmt.Fprintf(&b, "  provider check [%s]: %s.%s: %s\n", check.Status, check.Provider.Package, check.Provider.Arch, check.Reason)
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
