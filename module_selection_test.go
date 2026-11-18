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

func moduleSelectionWork(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	t.Cleanup(func() { _ = removeAggregateWork(work) })
	return work
}

func moduleSelectionFixture(t *testing.T) moduleSelectionRequest {
	t.Helper()
	proxy := t.TempDir()
	root := module.Version{Path: "example.test/project", Version: "v1.0.0"}
	direct := module.Version{Path: "example.test/direct", Version: "v1.2.0"}
	transitive := module.Version{Path: "example.test/transitive", Version: "v1.1.0"}
	testOnly := module.Version{Path: "example.test/testonly", Version: "v1.0.0"}
	unused := module.Version{Path: "example.test/unused", Version: "v1.3.0"}
	amdOnly := module.Version{Path: "example.test/amdonly", Version: "v1.0.0"}
	riscvOnly := module.Version{Path: "example.test/riscvonly", Version: "v1.0.0"}
	cliOnly := module.Version{Path: "example.test/cli-only", Version: "v1.0.0"}
	modules := []struct {
		mod   module.Version
		files map[string]string
	}{
		{root, map[string]string{
			"go.mod":              "module example.test/project\n\ngo 1.23.0\n\nrequire (\n\texample.test/direct v1.2.0\n\texample.test/testonly v1.0.0\n\texample.test/unused v1.3.0\n\texample.test/amdonly v1.0.0\n\texample.test/riscvonly v1.0.0\n\texample.test/cli-only v1.0.0\n)\n",
			"project.go":          "package project\nimport _ \"example.test/direct/pkg\"\n",
			"platform_amd64.go":   "package project\nimport _ \"example.test/amdonly/pkg\"\n",
			"platform_riscv64.go": "package project\nimport _ \"example.test/riscvonly/pkg\"\n",
			"cmd/main.go":         "package main\nimport _ \"example.test/cli-only/pkg\"\nfunc main() {}\n",
			"project_test.go":     "package project\nimport (\n\t\"testing\"\n\t_ \"example.test/testonly/pkg\"\n)\nfunc TestFixture(t *testing.T) {}\n",
		}},
		{direct, map[string]string{
			"go.mod":        "module example.test/direct\n\ngo 1.23.0\n\nrequire example.test/transitive v1.1.0\n",
			"pkg/direct.go": "package pkg\nimport _ \"example.test/transitive/pkg\"\n",
		}},
		{transitive, map[string]string{
			"go.mod":            "module example.test/transitive\n\ngo 1.23.0\n",
			"pkg/transitive.go": "package pkg\n",
		}},
		{testOnly, map[string]string{
			"go.mod":          "module example.test/testonly\n\ngo 1.23.0\n",
			"pkg/testonly.go": "package pkg\n",
		}},
		{unused, map[string]string{
			"go.mod":    "module example.test/unused\n\ngo 1.23.0\n",
			"unused.go": "package unused\n",
		}},
	}
	modules = append(modules,
		struct {
			mod   module.Version
			files map[string]string
		}{amdOnly, map[string]string{
			"go.mod":     "module example.test/amdonly\n\ngo 1.23.0\n",
			"pkg/amd.go": "package pkg\n",
		}},
		struct {
			mod   module.Version
			files map[string]string
		}{riscvOnly, map[string]string{
			"go.mod":       "module example.test/riscvonly\n\ngo 1.23.0\n",
			"pkg/riscv.go": "package pkg\n",
		}},
	)
	modules = append(modules, struct {
		mod   module.Version
		files map[string]string
	}{cliOnly, map[string]string{
		"go.mod":     "module example.test/cli-only\n\ngo 1.23.0\n",
		"pkg/cli.go": "package pkg\n",
	}})
	for _, item := range modules {
		aggregateProxyModule(t, proxy, item.mod, item.files)
	}
	unreferencedNested := module.Version{Path: "example.test/project/nested", Version: "v0.1.0"}
	aggregateProxyModule(t, proxy, unreferencedNested, map[string]string{
		"go.mod":    "module example.test/project/nested\n\ngo 1.23.0\n",
		"nested.go": "package nested\n",
	})
	aggregateProxyEnv(t, proxy)
	request, err := parseModuleSelectionRequest(root.Path + "@" + root.Version)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestValidateModuleSelectionResolvedPinRequiresExactCommitOrigin(t *testing.T) {
	pin := "0123456789abcdef0123456789abcdef01234567"
	request, err := parseModuleSelectionRequest("example.test/project@" + pin)
	if err != nil {
		t.Fatal(err)
	}
	resolved := "v1.0.1-0.20200101000000-0123456789ab"
	if err := validateModuleSelectionResolvedPin(request, resolved, &moduleOrigin{Hash: pin}); err != nil {
		t.Fatalf("matching full commit origin was rejected: %v", err)
	}
	for _, origins := range [][]*moduleOrigin{
		{nil},
		{{Hash: "0123456789ab0000000000000000000000000000"}},
	} {
		if err := validateModuleSelectionResolvedPin(request, resolved, origins...); err == nil {
			t.Errorf("unverified commit origin accepted: %+v", origins)
		}
	}

	versionRequest, err := parseModuleSelectionRequest("example.test/project@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateModuleSelectionResolvedPin(versionRequest, "v1.2.4"); err == nil {
		t.Fatal("Go version resolution silently changed the requested release")
	}
}

func TestParseModuleSelectionRequestRequiresPinnedVersionOrFullCommit(t *testing.T) {
	for _, input := range []string{
		"example.test/project@latest",
		"example.test/project@main",
		"example.test/project@v1.2",
		"example.test/project@abcdef",
		"example.test/project@v1.0.0+meta",
		"example.test/../project@v1.0.0",
	} {
		if _, err := parseModuleSelectionRequest(input); err == nil {
			t.Errorf("accepted unpinned or invalid request %q", input)
		}
	}
	request, err := parseModuleSelectionRequest("example.test/project@v1.2.3")
	if err != nil || request.RequestedPin != "v1.2.3" || request.CommitRequest {
		t.Fatalf("canonical version request = %+v, %v", request, err)
	}
	request, err = parseModuleSelectionRequest("example.test/project@0123456789abcdef0123456789abcdef01234567")
	if err != nil || !request.CommitRequest {
		t.Fatalf("full commit request = %+v, %v", request, err)
	}
}

func TestValidateModuleSelectionPackageScope(t *testing.T) {
	got, err := validateModuleSelectionPackages("example.test/project", []string{"example.test/project/z", "example.test/project/a", "example.test/project/z"})
	if err != nil || !reflect.DeepEqual(got, []string{"example.test/project/a", "example.test/project/z"}) {
		t.Fatalf("normalized package scope=%v err=%v", got, err)
	}
	for _, pkg := range []string{"example.test/project-extra/pkg", "example.test/outside", "./...", "example.test/project/../outside"} {
		if _, err := validateModuleSelectionPackages("example.test/project", []string{pkg}); err == nil {
			t.Errorf("accepted package outside the project module: %q", pkg)
		}
	}
}

func TestModuleSelectionCandidatesUseGoPackageOwnershipAndReportGraphOnlyModules(t *testing.T) {
	root := "example.test/project"
	graph := []moduleSelectionGraphModule{
		{Path: root, Main: true},
		{Path: "example.test/direct", Version: "v1.2.0"},
		{Path: "example.test/transitive", Version: "v1.1.0"},
		{Path: "example.test/testonly", Version: "v1.0.0"},
		{Path: "example.test/unused", Version: "v1.3.0"},
	}
	byArch := map[string][]moduleSelectionGoPackage{
		"amd64": {
			{ImportPath: root, Module: &moduleSelectionGraphModule{Path: root, Main: true}, Imports: []string{"example.test/direct/pkg"}, TestImports: []string{"example.test/testonly/pkg"}},
			{ImportPath: "example.test/direct/pkg", Module: &moduleSelectionGraphModule{Path: "example.test/direct", Version: "v1.2.0"}, Imports: []string{"example.test/transitive/pkg"}},
			{ImportPath: "example.test/transitive/pkg", Module: &moduleSelectionGraphModule{Path: "example.test/transitive", Version: "v1.1.0"}},
			{ImportPath: "example.test/testonly/pkg", Module: &moduleSelectionGraphModule{Path: "example.test/testonly", Version: "v1.0.0"}},
		},
		"riscv64": {
			{ImportPath: root, Module: &moduleSelectionGraphModule{Path: root, Main: true}, Imports: []string{"example.test/direct/pkg"}, TestImports: []string{"example.test/testonly/pkg"}},
			{ImportPath: "example.test/direct/pkg", Module: &moduleSelectionGraphModule{Path: "example.test/direct", Version: "v1.2.0"}, Imports: []string{"example.test/transitive/pkg"}},
			{ImportPath: "example.test/transitive/pkg", Module: &moduleSelectionGraphModule{Path: "example.test/transitive", Version: "v1.1.0"}},
			{ImportPath: "example.test/testonly/pkg", Module: &moduleSelectionGraphModule{Path: "example.test/testonly", Version: "v1.0.0"}},
		},
	}
	requirements := map[string]moduleSelectionRequirement{
		"example.test/direct":   {Version: "v1.0.0"},
		"example.test/testonly": {Version: "v1.0.0", Indirect: true},
		"example.test/unused":   {Version: "v1.3.0"},
	}
	candidates, err := moduleSelectionCandidates(root, graph, requirements, byArch)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]moduleSelectionCandidate)
	for _, candidate := range candidates {
		byPath[candidate.Path] = candidate
	}
	if got := byPath["example.test/direct"]; got.SelectedVersion != "v1.2.0" || got.DeclaredRequirement != "v1.0.0" || got.RequirementDirectness != "direct" || !slicesContains(got.Usage, "production-import") {
		t.Errorf("direct candidate does not report MVS/version/use evidence: %+v", got)
	}
	if got := byPath["example.test/transitive"]; !slicesContains(got.Usage, "transitive") || !reflect.DeepEqual(got.Architectures[0].TransitiveImports, []string{"example.test/transitive/pkg"}) {
		t.Errorf("transitive candidate evidence is incorrect: %+v", got)
	}
	if got := byPath["example.test/testonly"]; !slicesContains(got.Usage, "test-import") || got.RequirementDirectness != "indirect" {
		t.Errorf("test-only candidate evidence is incorrect: %+v", got)
	}
	if got := byPath["example.test/unused"]; !slicesContains(got.Usage, "declared-unused") || len(got.Architectures) != 0 {
		t.Errorf("unused module was treated as used: %+v", got)
	}
}

func slicesContains(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func TestModuleSelectionReportUsesLocalFileProxyAndExcludesNestedModuleFromRootScan(t *testing.T) {
	request := moduleSelectionFixture(t)
	work := moduleSelectionWork(t)
	report, err := moduleSelectionReportFor(context.Background(), work, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Project.Path != request.Path || report.Project.RequestedPin != request.RequestedPin || report.Project.Resolved != request.RequestedPin {
		t.Fatalf("report changed the pinned project identity: %+v", report.Project)
	}
	if !reflect.DeepEqual(report.PackageScope, []string{"./..."}) {
		t.Fatalf("default package scope=%v", report.PackageScope)
	}
	byPath := make(map[string]moduleSelectionCandidate)
	for _, candidate := range report.Candidates {
		byPath[candidate.Path] = candidate
	}
	for _, want := range []string{"example.test/direct", "example.test/transitive", "example.test/testonly", "example.test/unused", "example.test/amdonly", "example.test/riscvonly"} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("module graph candidate %q missing; got %v", want, moduleSelectionCandidatePaths(report.Candidates))
		}
	}
	if _, ok := byPath["example.test/project/nested"]; ok {
		t.Fatal("unreferenced nested go.mod was selected as a project module")
	}
	if !slicesContains(byPath["example.test/unused"].Usage, "declared-unused") {
		t.Fatalf("unimported direct requirement was not marked unused: %+v", byPath["example.test/unused"])
	}
	if !slicesContains(byPath["example.test/cli-only"].Usage, "production-import") {
		t.Fatalf("subpackage-only import was not visible in the default ./... scope: %+v", byPath["example.test/cli-only"])
	}
	selectedReport, err := moduleSelectionReportFor(context.Background(), moduleSelectionWork(t), request, []string{request.Path})
	if err != nil {
		t.Fatal(err)
	}
	var selectedCLICandidate *moduleSelectionCandidate
	for i := range selectedReport.Candidates {
		if selectedReport.Candidates[i].Path == "example.test/cli-only" {
			selectedCLICandidate = &selectedReport.Candidates[i]
			break
		}
	}
	if selectedCLICandidate == nil || !slicesContains(selectedCLICandidate.Usage, "declared-unused") || len(selectedCLICandidate.Architectures) != 0 {
		t.Fatalf("package-scoped analysis attributed an unselected subpackage dependency: %+v", selectedCLICandidate)
	}
	amdEvidence := byPath["example.test/amdonly"].Architectures
	riscvEvidence := byPath["example.test/riscvonly"].Architectures
	if len(amdEvidence) != 1 || amdEvidence[0].Architecture != "amd64" || !reflect.DeepEqual(amdEvidence[0].ProductionImports, []string{"example.test/amdonly/pkg"}) {
		t.Fatalf("amd64-specific dependency evidence is incorrect: %+v", byPath["example.test/amdonly"])
	}
	if len(riscvEvidence) != 1 || riscvEvidence[0].Architecture != "riscv64" || !reflect.DeepEqual(riscvEvidence[0].ProductionImports, []string{"example.test/riscvonly/pkg"}) {
		t.Fatalf("riscv64-specific dependency evidence is incorrect: %+v", byPath["example.test/riscvonly"])
	}
	if _, err := os.Stat(filepath.Join(work, "go-example.spec")); !os.IsNotExist(err) {
		t.Fatalf("module selection created an RPM spec: %v", err)
	}
	first, err := moduleSelectionReportJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := moduleSelectionReportJSON(report)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("report JSON was not stable: %v", err)
	}
	var decoded moduleSelectionReport
	if err := json.Unmarshal(first, &decoded); err != nil || len(decoded.Candidates) != len(report.Candidates) {
		t.Fatalf("JSON report decode failed: %v", err)
	}
}

func moduleSelectionCandidatePaths(candidates []moduleSelectionCandidate) []string {
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.Path)
	}
	return paths
}

func TestModuleSelectionRejectsReplaceAndExcludeWithoutPartialReport(t *testing.T) {
	for _, directive := range []string{
		"replace example.test/direct => example.test/fork v1.2.0",
		"exclude example.test/direct v1.2.0",
	} {
		t.Run(directive, func(t *testing.T) {
			proxy := t.TempDir()
			mod := module.Version{Path: "example.test/replace-project", Version: "v1.0.0"}
			aggregateProxyModule(t, proxy, mod, map[string]string{
				"go.mod":  "module " + mod.Path + "\n\ngo 1.23.0\n\n" + directive + "\n",
				"main.go": "package project\n",
			})
			aggregateProxyEnv(t, proxy)
			request, err := parseModuleSelectionRequest(mod.Path + "@" + mod.Version)
			if err != nil {
				t.Fatal(err)
			}
			work := moduleSelectionWork(t)
			report, err := moduleSelectionReportFor(context.Background(), work, request, nil)
			if err == nil || !strings.Contains(err.Error(), "replace or exclude") {
				t.Fatalf("unsupported directive was not rejected: %v", err)
			}
			if report.Schema != 0 || len(report.Candidates) != 0 {
				t.Fatalf("unsupported project returned a partial report: %+v", report)
			}
		})
	}
}
