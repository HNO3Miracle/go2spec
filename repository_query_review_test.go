package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func TestParseRPMEVRValidatesRPMLabels(t *testing.T) {
	for _, input := range []string{"1.2.3", "0:1.2.3-4.or", "1.2.3~rc1", "1.2.3^git2", "1.2+abc-1_2"} {
		if _, err := parseRPMEVR(input); err != nil {
			t.Errorf("valid RPM EVR %q rejected: %v", input, err)
		}
	}
	for _, input := range []string{"1!", "1:2:3", "1.2:bad", "1.2,3", "1.2-", ":1", "-1", "foo-!", "x:1.0"} {
		if _, err := parseRPMEVR(input); err == nil {
			t.Errorf("malformed RPM EVR %q was accepted", input)
		}
	}
}

func TestMalformedProviderEVRProducesUnknown(t *testing.T) {
	provider := rpmProvider{
		Capability: "go(example.test/lib)", Package: "go-lib", Arch: "noarch",
		Flags: "EQ", ProvideEVR: &rpmEVR{Epoch: "0", Version: "1!"},
	}
	status, _, checks := diagnoseRepositoryProviderDetails(context.Background(), []rpmProvider{provider}, &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Epoch: "0", Version: "1.0"}}, compareRPMEVR)
	if status != "unknown" || len(checks) != 1 || checks[0].Status != "unknown" {
		t.Fatalf("malformed provider EVR result = %s, %+v", status, checks)
	}
}

func TestReadRepositorySpecEvaluatesArchitectureConditionals(t *testing.T) {
	spec := `BuildRequires: go(example.test/common)
%ifarch x86_64
BuildRequires: go(example.test/x86)
%else
BuildRequires: go(example.test/not-x86)
%endif
%ifnarch x86_64
Requires: go(example.test/non-x86)
%endif
`
	name := filepath.Join(t.TempDir(), "conditional.spec")
	if err := os.WriteFile(name, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		arch string
		want []string
	}{
		{arch: "x86_64", want: []string{"go(example.test/common)", "go(example.test/x86)"}},
		{arch: "riscv64", want: []string{"go(example.test/common)", "go(example.test/not-x86)", "go(example.test/non-x86)"}},
	} {
		deps, errorsFound, err := readRepositorySpec(name, tc.arch)
		if err != nil || len(errorsFound) != 0 {
			t.Fatalf("arch %s: deps=%+v errors=%v err=%v", tc.arch, deps, errorsFound, err)
		}
		got := make([]string, 0, len(deps))
		for _, dep := range deps {
			got = append(got, dep.Capability)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("arch %s dependencies=%v, want %v", tc.arch, got, tc.want)
		}
	}
}

func TestReadRepositorySpecReportsUnresolvedConditionsAndMacros(t *testing.T) {
	spec := `%if 0%{?some_build_condition}
BuildRequires: go(example.test/conditional)
%endif
BuildRequires: %{go_deps}
`
	name := filepath.Join(t.TempDir(), "unresolved.spec")
	if err := os.WriteFile(name, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	deps, errorsFound, err := readRepositorySpec(name, "x86_64")
	if err != nil || len(deps) != 2 || len(errorsFound) != 2 {
		t.Fatalf("deps=%+v errors=%v err=%v", deps, errorsFound, err)
	}
	for _, dep := range deps {
		if dep.InputError == "" {
			t.Errorf("unresolved dependency %q has no diagnostic", dep.Capability)
		}
	}
}

func TestConflictingModuleRequirementsAreNeverSelected(t *testing.T) {
	dependencies := []repositoryDependency{
		{Capability: "go(example.test/sdk/pkg)", Scope: "build"},
		{Capability: "go(example.test/sdk/sub/leaf)", Scope: "build"},
	}
	modules := []module.Version{
		{Path: "example.test/sdk", Version: "v1.0.0"},
		{Path: "example.test/sdk", Version: "v1.1.0"},
		{Path: "example.test/sdk/sub", Version: "v2.0.0"},
		{Path: "example.test/sdk/sub", Version: "v2.1.0"},
	}
	firstErrors := attachRepositoryModuleRequirements(dependencies, modules)
	if len(firstErrors) != 2 {
		t.Fatalf("conflict errors=%v", firstErrors)
	}
	for _, dep := range dependencies {
		if dep.GoModule != nil || !strings.Contains(dep.InputError, "conflicting --module-require") {
			t.Errorf("conflict selected a module version: %+v", dep)
		}
	}
	reversedDependencies := []repositoryDependency{
		{Capability: "go(example.test/sdk/pkg)", Scope: "build"},
		{Capability: "go(example.test/sdk/sub/leaf)", Scope: "build"},
	}
	reversedModules := []module.Version{modules[3], modules[2], modules[1], modules[0]}
	secondErrors := attachRepositoryModuleRequirements(reversedDependencies, reversedModules)
	if !reflect.DeepEqual(firstErrors, secondErrors) {
		t.Fatalf("conflict errors depend on input order: %v vs %v", firstErrors, secondErrors)
	}
}

func TestMultipleProvidersPreservePerProviderVersionResults(t *testing.T) {
	provider := func(name, version string) rpmProvider {
		return rpmProvider{Capability: "go(example.test/lib)", Package: name, Arch: "noarch", Flags: "EQ", ProvideEVR: &rpmEVR{Epoch: "0", Version: version}}
	}
	compare := func(_ context.Context, left, right rpmEVR) (int, error) {
		return strings.Compare(left.Version, right.Version), nil
	}
	requirement := &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Epoch: "0", Version: "2.0"}}
	cases := []struct {
		name      string
		providers []rpmProvider
		statuses  []string
	}{
		{name: "all insufficient", providers: []rpmProvider{provider("one", "1.0"), provider("two", "1.5")}, statuses: []string{"version-insufficient", "version-insufficient"}},
		{name: "mixed", providers: []rpmProvider{provider("one", "1.0"), provider("two", "2.0")}, statuses: []string{"version-insufficient", "satisfied"}},
		{name: "one unknown", providers: []rpmProvider{provider("one", "1.0"), {Capability: "go(example.test/lib)", Package: "two", Arch: "noarch"}}, statuses: []string{"version-insufficient", "unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, checks := diagnoseRepositoryProviderDetails(context.Background(), tc.providers, requirement, compare)
			if status != "multiple-providers" || len(checks) != len(tc.statuses) {
				t.Fatalf("overall status=%s checks=%+v", status, checks)
			}
			for i, want := range tc.statuses {
				if checks[i].Status != want {
					t.Errorf("provider %d status=%s, want %s", i, checks[i].Status, want)
				}
			}
		})
	}
}

func TestRepositoryReportSortsTiesAndUsesStableModuleJSON(t *testing.T) {
	catalog := repositoryCatalog{
		Snapshot:  repositorySnapshot{Distro: "fixture", Repository: "repo", Arch: "x86_64"},
		Providers: map[string][]rpmProvider{"go(example.test/lib)": {{Capability: "go(example.test/lib)", Package: "go-lib", Arch: "noarch"}}},
	}
	dependencies := []repositoryDependency{
		{Capability: "go(example.test/lib)", Scope: "build", Requirement: &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Epoch: "0", Version: "2.0"}}, GoModule: &module.Version{Path: "example.test/lib", Version: "v2.0.0"}},
		{Capability: "go(example.test/lib)", Scope: "build", Requirement: &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Epoch: "0", Version: "1.0"}}, GoModule: &module.Version{Path: "example.test/lib", Version: "v1.0.0"}},
	}
	first, err := reportJSON(diagnoseRepositoryDependencies(context.Background(), catalog, dependencies, compareRPMEVR))
	if err != nil {
		t.Fatal(err)
	}
	slicesReverseDependencies(dependencies)
	second, err := reportJSON(diagnoseRepositoryDependencies(context.Background(), catalog, dependencies, compareRPMEVR))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("report order changed with input order:\n%s\n%s", first, second)
	}
	var decoded repositoryQueryReport
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Dependencies) != 2 || decoded.Dependencies[0].Requirement.EVR.Version != "1.0" || decoded.Dependencies[0].GoModule.Version != "v1.0.0" {
		t.Fatalf("requirements were not sorted deterministically: %+v", decoded.Dependencies)
	}
	if !strings.Contains(string(first), `"go_module": {`+"\n"+`        "path": "example.test/lib"`) {
		t.Fatalf("Module JSON fields do not use the stable schema: %s", first)
	}
}

func TestRepositoryTextReportUsesChecksumAlgorithmsAndSizes(t *testing.T) {
	report := repositoryQueryReport{Snapshot: repositorySnapshot{
		Repository: "repo", Distro: "fixture", Arch: "x86_64", Revision: "1", RepomdSHA256: "repomd",
		PrimaryChecksumType: "sha512", PrimaryChecksum: "compressed", PrimarySize: 123,
		PrimaryOpenChecksumType: "sha512", PrimaryOpenChecksum: "open", PrimaryOpenChecksumStatus: "verified", PrimaryOpenSize: 456,
	}}
	text := renderRepositoryReport(report)
	for _, expected := range []string{"primary checksum=sha512:compressed (123 bytes)", "open checksum=sha512:open (456 bytes, verified)", "signature="} {
		if !strings.Contains(text, expected) {
			t.Errorf("report omitted %q: %s", expected, text)
		}
	}
}

func TestModuleRequirementReportsUnmatchedAndTracksMatchedVersions(t *testing.T) {
	dependencies := []repositoryDependency{
		{Capability: "go(example.test/sdk/pkg)", Scope: "build"},
		{Capability: "go(example.test/sdk/sub/client)", Scope: "build"},
	}
	modules := []module.Version{
		{Path: "example.test/sdk", Version: "v1.0.0"},
		{Path: "example.test/sdk/sub", Version: "v2.0.0"},
		{Path: "example.test/typo", Version: "v1.0.0"},
	}
	gotErrors := attachRepositoryModuleRequirements(dependencies, modules)
	wantError := "--module-require example.test/typo@v1.0.0 does not match any queried Go package capability"
	if !reflect.DeepEqual(gotErrors, []string{wantError}) {
		t.Fatalf("unmatched module errors=%v, want %q", gotErrors, wantError)
	}
	if dependencies[0].GoModule == nil || dependencies[0].GoModule.Path != "example.test/sdk" {
		t.Fatalf("root module was not matched: %+v", dependencies[0])
	}
	if dependencies[1].GoModule == nil || dependencies[1].GoModule.Path != "example.test/sdk/sub" {
		t.Fatalf("longest nested module was not selected: %+v", dependencies[1])
	}

	reversed := append([]module.Version(nil), modules...)
	slices.Reverse(reversed)
	reversedDependencies := []repositoryDependency{
		{Capability: "go(example.test/sdk/pkg)", Scope: "build"},
		{Capability: "go(example.test/sdk/sub/client)", Scope: "build"},
	}
	reversedErrors := attachRepositoryModuleRequirements(reversedDependencies, reversed)
	if !reflect.DeepEqual(gotErrors, reversedErrors) {
		t.Fatalf("unmatched module diagnostics depend on input order: %v vs %v", gotErrors, reversedErrors)
	}
	if !reflect.DeepEqual(modules, []module.Version{
		{Path: "example.test/sdk", Version: "v1.0.0"},
		{Path: "example.test/sdk/sub", Version: "v2.0.0"},
		{Path: "example.test/typo", Version: "v1.0.0"},
	}) {
		t.Fatalf("module input order was mutated: %+v", modules)
	}
}

func TestQueryDependenciesReportsUnmatchedModuleRequirement(t *testing.T) {
	dependencies, errorsFound, err := queryDependenciesFromInputs(
		"", "x86_64", []string{"go(example.test/queried/pkg)"}, nil,
		[]string{"example.test/typo@v1.0.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependencies) != 1 || len(errorsFound) != 1 || !strings.Contains(errorsFound[0], "example.test/typo@v1.0.0") {
		t.Fatalf("unmatched module requirement was not surfaced: dependencies=%+v errors=%v", dependencies, errorsFound)
	}
}

func TestReadRepositorySpecEvaluatesCommaSeparatedArchLists(t *testing.T) {
	spec := `%ifarch x86_64,riscv64
BuildRequires: go(example.test/positive)
%endif
%ifarch x86_64, riscv64
Requires: go(example.test/positive-spaced)
%endif
%ifnarch x86_64,riscv64
BuildRequires: go(example.test/negative)
%endif
%ifnarch aarch64, ppc64le
Requires: go(example.test/nonmatching-negative)
%endif
%ifarch aarch64,ppc64le
Requires: go(example.test/nonmatching-positive)
%endif
`
	name := filepath.Join(t.TempDir(), "comma-arch.spec")
	if err := os.WriteFile(name, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		arch string
		want []string
	}{
		{arch: "x86_64", want: []string{"go(example.test/positive)", "go(example.test/positive-spaced)", "go(example.test/nonmatching-negative)"}},
		{arch: "riscv64", want: []string{"go(example.test/positive)", "go(example.test/positive-spaced)", "go(example.test/nonmatching-negative)"}},
	} {
		dependencies, errorsFound, err := readRepositorySpec(name, tc.arch)
		if err != nil || len(errorsFound) != 0 {
			t.Fatalf("arch %s: dependencies=%+v errors=%v err=%v", tc.arch, dependencies, errorsFound, err)
		}
		got := make([]string, 0, len(dependencies))
		for _, dependency := range dependencies {
			got = append(got, dependency.Capability)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("arch %s dependencies=%v, want %v", tc.arch, got, tc.want)
		}
	}
}

func TestReadRepositorySpecReportsMalformedArchListAsUnknown(t *testing.T) {
	name := filepath.Join(t.TempDir(), "malformed-arch.spec")
	if err := os.WriteFile(name, []byte("%ifarch x86_64,,riscv64\nBuildRequires: go(example.test/uncertain)\n%endif\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dependencies, errorsFound, err := readRepositorySpec(name, "x86_64")
	if err != nil || len(dependencies) != 1 || len(errorsFound) != 1 || dependencies[0].InputError == "" {
		t.Fatalf("malformed architecture list was not diagnosed: dependencies=%+v errors=%v err=%v", dependencies, errorsFound, err)
	}
}
