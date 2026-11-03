package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func TestReadRepositorySpecDependencies(t *testing.T) {
	spec := `Name: example-consumer
BuildRequires:  go(example.test/lib/sub)
BuildRequires:  go(example.test/lib/other) >= 0:2.1.0-3.or
Requires:       go(example.test/runtime/api)
`
	name := filepath.Join(t.TempDir(), "consumer.spec")
	if err := os.WriteFile(name, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	dependencies, errorsFound, err := readRepositorySpec(name, "x86_64")
	if err != nil || len(errorsFound) != 0 || len(dependencies) != 3 {
		t.Fatalf("dependencies=%+v errors=%v err=%v", dependencies, errorsFound, err)
	}
	if dependencies[0].Scope != "build" || dependencies[1].Requirement == nil || dependencies[2].Scope != "runtime" {
		t.Fatalf("wrong dependency fields: %+v", dependencies)
	}
	if got := formatRPMEVR(dependencies[1].Requirement.EVR); got != "2.1.0-3.or" {
		t.Fatalf("parsed EVR = %q", got)
	}
}

func TestReadRepositorySpecDoesNotSilentlyDropUnparsedGoDependency(t *testing.T) {
	name := filepath.Join(t.TempDir(), "consumer.spec")
	if err := os.WriteFile(name, []byte("BuildRequires: go(%{dynamic_import_path})\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dependencies, errorsFound, err := readRepositorySpec(name, "x86_64")
	if err != nil || len(dependencies) != 1 || len(errorsFound) != 1 || dependencies[0].InputError == "" {
		t.Fatalf("dependencies=%+v errors=%v err=%v", dependencies, errorsFound, err)
	}
}

func TestParseRepositoryRequirementAndModuleMinimum(t *testing.T) {
	dependency, err := parseRepositoryRPMRequirement("go(example.test/lib/sub) >= 0:1.2.3-4.or")
	if err != nil {
		t.Fatal(err)
	}
	if dependency.Capability != "go(example.test/lib/sub)" || dependency.Requirement.Operator != ">=" || formatRPMEVR(dependency.Requirement.EVR) != "1.2.3-4.or" {
		t.Fatalf("wrong explicit requirement: %+v", dependency)
	}
	for _, input := range []string{
		"go(example.test/lib) >= %{version}",
		"go(example.test/lib) != 1.2.3",
		"go(example.test/lib/sub",
	} {
		if _, err := parseRepositoryRPMRequirement(input); err == nil {
			t.Errorf("accepted malformed requirement %q", input)
		}
	}
	for _, input := range []string{"example.test/lib@v1.2.3", "example.test/lib@latest", "example.test/lib@v1.2"} {
		if _, err := parseRepositoryModuleRequirement(input); err != nil && input == "example.test/lib@v1.2.3" {
			t.Fatalf("valid Go module requirement rejected: %v", err)
		} else if err == nil && input != "example.test/lib@v1.2.3" {
			t.Errorf("noncanonical Go requirement accepted: %s", input)
		}
	}
}

func TestAttachRepositoryModuleRequirementsUsesLongestImportRoot(t *testing.T) {
	dependencies := []repositoryDependency{
		{Capability: "go(example.test/sdk/sub/client)", Scope: "build"},
		{Capability: "go(example.test/sdk/submarine/client)", Scope: "runtime"},
		{Capability: "go(example.test/sdk/elsewhere)", Scope: "build"},
	}
	modules := []module.Version{
		{Path: "example.test/sdk", Version: "v1.0.0"},
		{Path: "example.test/sdk/sub", Version: "v2.3.4"},
	}
	attachRepositoryModuleRequirements(dependencies, modules)
	if dependencies[0].GoModule == nil || dependencies[0].GoModule.Path != "example.test/sdk/sub" {
		t.Fatalf("nested package mapped to the wrong module: %+v", dependencies[0])
	}
	if dependencies[1].GoModule == nil || dependencies[1].GoModule.Path != "example.test/sdk" {
		t.Fatalf("slash-boundary mapping failed: %+v", dependencies[1])
	}
	if dependencies[2].GoModule == nil || dependencies[2].GoModule.Path != "example.test/sdk" {
		t.Fatalf("root module mapping failed: %+v", dependencies[2])
	}
}

func TestGoModuleVersionDoesNotPretendToBeRPMSatisfied(t *testing.T) {
	catalog := repositoryCatalog{
		Snapshot: repositorySnapshot{Distro: "fixture", Repository: "repo", Arch: "x86_64"},
		Providers: map[string][]rpmProvider{
			"go(example.test/lib/sub)": {{Capability: "go(example.test/lib/sub)", Package: "go-lib", Arch: "noarch", ProvideEVR: &rpmEVR{Epoch: "0", Version: "99.0"}, Flags: "EQ"}},
		},
	}
	dependencies := []repositoryDependency{{
		Capability: "go(example.test/lib/sub)", Scope: "build",
		GoModule: &module.Version{Path: "example.test/lib", Version: "v1.2.3"},
	}}
	report := diagnoseRepositoryDependencies(context.Background(), catalog, dependencies, compareRPMEVR)
	if len(report.Dependencies) != 1 || report.Dependencies[0].Status != "unknown" || report.Dependencies[0].VersionBasis != "go-module-version-not-comparable-to-rpm-evr" {
		t.Fatalf("Go module requirement was treated as RPM EVR: %+v", report.Dependencies)
	}
}

func TestRepositoryReportJSONIsDeterministicAndCarriesSnapshot(t *testing.T) {
	catalog := repositoryCatalog{
		Snapshot: repositorySnapshot{Distro: "openruyi", Repository: "snapshot", Arch: "x86_64", Revision: "42", RepomdSHA256: "abc", SignatureStatus: "verified"},
		Providers: map[string][]rpmProvider{
			"go(example.test/a)": {
				{Capability: "go(example.test/a)", Package: "z-provider", Arch: "noarch"},
				{Capability: "go(example.test/a)", Package: "a-provider", Arch: "x86_64"},
			},
		},
	}
	deps := []repositoryDependency{{Capability: "go(example.test/a)", Scope: "build"}, {Capability: "go(example.test/missing)", Scope: "runtime"}}
	first := diagnoseRepositoryDependencies(context.Background(), catalog, deps, compareRPMEVR)
	slicesReverseDependencies(deps)
	second := diagnoseRepositoryDependencies(context.Background(), catalog, deps, compareRPMEVR)
	firstJSON, err := reportJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := reportJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstJSON, secondJSON) {
		t.Fatalf("query ordering changed report JSON:\n%s\n%s", firstJSON, secondJSON)
	}
	var decoded repositoryQueryReport
	if err := json.Unmarshal(firstJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Snapshot.Distro != "openruyi" || decoded.Snapshot.Revision != "42" || decoded.Dependencies[0].Status != "multiple-providers" || decoded.Dependencies[1].Status != "missing" {
		t.Fatalf("unexpected deterministic report: %+v", decoded)
	}
	if !strings.Contains(renderRepositoryReport(decoded), "repomd revision=42") {
		t.Fatal("text report omitted snapshot identity")
	}
}

func slicesReverseDependencies(dependencies []repositoryDependency) {
	for left, right := 0, len(dependencies)-1; left < right; left, right = left+1, right-1 {
		dependencies[left], dependencies[right] = dependencies[right], dependencies[left]
	}
}

func TestRPMNativeComparatorHandlesVersionOnlyEVR(t *testing.T) {
	cmp, err := compareRPMEVR(context.Background(), rpmEVR{Epoch: "0", Version: "1.2.3"}, rpmEVR{Epoch: "0", Version: "1.2.0"})
	if err != nil || cmp <= 0 {
		t.Fatalf("version-only RPM comparison failed: cmp=%d err=%v", cmp, err)
	}
	if _, err := compareRPMEVR(context.Background(), rpmEVR{Epoch: "0", Version: "1.2.3"}, rpmEVR{Epoch: "0", Version: "1.2.3", Release: "1"}); err == nil {
		t.Fatal("partially specified RPM release was treated as comparable")
	}
}

func TestRPMRequirementSatisfactionRequiresVersionedEQProvide(t *testing.T) {
	requirement := &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Epoch: "0", Version: "1.2.3"}}
	compare := func(_ context.Context, left, right rpmEVR) (int, error) {
		if left.Version < right.Version {
			return -1, nil
		}
		if left.Version > right.Version {
			return 1, nil
		}
		return 0, nil
	}
	for _, provider := range []rpmProvider{
		{Flags: "EQ", ProvideEVR: &rpmEVR{Epoch: "0", Version: "1.2.4"}},
		{Flags: "GE", ProvideEVR: &rpmEVR{Epoch: "0", Version: "1.2.4"}},
		{Flags: "EQ"},
	} {
		_, comparable, err := rpmRequirementSatisfied(context.Background(), provider, requirement, compare)
		if err != nil {
			t.Fatal(err)
		}
		if provider.Flags == "EQ" && provider.ProvideEVR != nil && !comparable {
			t.Fatal("versioned EQ provider should be comparable")
		}
		if (provider.Flags != "EQ" || provider.ProvideEVR == nil) && comparable {
			t.Fatalf("ambiguous provider flags or unversioned provide treated as comparable: %+v", provider)
		}
	}
}
