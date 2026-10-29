package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/build"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func aggregateWriteFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func aggregateZIP(t *testing.T, mod module.Version, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := writer.Create(mod.Path + "@" + mod.Version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func aggregateProxyModule(t *testing.T, proxy string, mod module.Version, files map[string]string) {
	t.Helper()
	p, err := module.EscapePath(mod.Path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := module.EscapeVersion(mod.Version)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(proxy, filepath.FromSlash(p), "@v", v)
	info, err := json.Marshal(map[string]string{"Version": mod.Version, "Time": "2020-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	aggregateWriteFile(t, root+".info", info)
	aggregateWriteFile(t, root+".mod", []byte(files["go.mod"]))
	aggregateWriteFile(t, root+".zip", aggregateZIP(t, mod, files))
}

func aggregateProxyEnv(t *testing.T, proxy string) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(proxy)}
	t.Setenv("GOPROXY", u.String())
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GONOSUMDB", "")
	t.Setenv("GOENV", "off")
	t.Setenv("GOEXPERIMENT", "")
}

func aggregateFixture(t *testing.T) (aggregateRequest, string) {
	t.Helper()
	proxy := t.TempDir()
	mods := []module.Version{{Path: "example.test/sdk/a", Version: "v1.2.3"}, {Path: "example.test/sdk/b", Version: "v0.2.0"}}
	for i, mod := range mods {
		files := map[string]string{
			"go.mod":        "module " + mod.Path + "\n\ngo 1.18\n",
			"LICENSE":       "Fixture license text\n",
			"value.go":      fmt.Sprintf("package value\n\nfunc Value() int { return %d }\n", i+1),
			"value_test.go": fmt.Sprintf("package value\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != %d { t.Fatal(Value()) } }\n", i+1),
		}
		aggregateProxyModule(t, proxy, mod, files)
	}
	aggregateProxyEnv(t, proxy)
	req, err := parseAggregateRequest("example.test/sdk", "go-example-sdk", "20261019", "MIT", []string{mods[1].Path + "@" + mods[1].Version, mods[0].Path + "@" + mods[0].Version})
	if err != nil {
		t.Fatal(err)
	}
	return req, proxy
}

func prepareAggregateTestArtifacts(t *testing.T, req aggregateRequest, parent string) string {
	t.Helper()
	output, err := prepareAggregate(context.Background(), req, parent)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	t.Cleanup(func() { _ = removeAggregateWork(work) })
	plan, err := buildAggregatePlan(context.Background(), req, work)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range plan.Sources {
		if err := copyFile(source.Zip, filepath.Join(output, source.FileName)); err != nil {
			t.Fatal(err)
		}
	}
	return output
}
func TestAggregatePrivateModulesNoPublicLookup(t *testing.T) {
	req, _ := aggregateFixture(t)
	req.License = ""
	t.Setenv("GOPRIVATE", "example.test/sdk")
	calls := aggregateUpdateTransport(t, nil)
	parent := t.TempDir()
	if _, err := prepareAggregate(context.Background(), req, parent); err == nil || !strings.Contains(err.Error(), "GOPRIVATE") {
		t.Fatalf("private module was not stopped before public metadata lookup: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("private module path sent to public service: %d requests", calls.Load())
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed private request left output: %v %v", entries, err)
	}
}

func TestAggregateGONOSUMDBModulesNoPublicLookup(t *testing.T) {
	req, _ := aggregateFixture(t)
	req.License = ""
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GONOSUMDB", "example.test/sdk")
	calls := aggregateUpdateTransport(t, nil)
	if _, err := prepareAggregate(context.Background(), req, t.TempDir()); err == nil || !strings.Contains(err.Error(), "GONOSUMDB") {
		t.Fatalf("GONOSUMDB module was not stopped before public metadata lookup: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("GONOSUMDB module path sent to public service: %d", calls.Load())
	}
}

func TestAggregateRejectsSelectedModuleBelowRequiredVersion(t *testing.T) {
	plan := aggregatePlan{Sources: []moduleSource{
		{Module: module.Version{Path: "example.test/sdk/a", Version: "v1.0.0"}, Requires: []module.Version{{Path: "example.test/sdk/b", Version: "v1.4.0"}}},
		{Module: module.Version{Path: "example.test/sdk/b", Version: "v1.1.0"}},
	}}
	if err := selectedModuleRequirementError(plan); err == nil || !strings.Contains(err.Error(), "v1.4.0") || !strings.Contains(err.Error(), "v1.1.0") {
		t.Fatalf("selected lower-than-required module was accepted: %v", err)
	}
	plan.Sources[1].Module.Version = "v1.4.0"
	if err := selectedModuleRequirementError(plan); err != nil {
		t.Fatalf("compatible selected module was rejected: %v", err)
	}
}

func TestAggregateProjectURLValidation(t *testing.T) {
	for _, raw := range []string{"http://example.test/project", "https://user:secret@example.test/project", "https://example.test/project?token=bad", "https://example.test/project#fragment", "https://example.test/project\nSource0:bad"} {
		if err := validateProjectURL(raw); err == nil {
			t.Errorf("accepted unsafe project URL %q", raw)
		}
	}
	if err := validateProjectURL("https://example.test/project"); err != nil {
		t.Fatal(err)
	}
}
func TestAggregateExistingOutputKinds(t *testing.T) {
	req, _ := aggregateFixture(t)
	for _, kind := range []string{"empty-directory", "file", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			final := filepath.Join(parent, req.Name)
			var err error
			switch kind {
			case "empty-directory":
				err = os.Mkdir(final, 0755)
			case "file":
				err = os.WriteFile(final, []byte("original"), 0644)
			case "dangling-symlink":
				err = os.Symlink(filepath.Join(parent, "absent"), final)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(final)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepareAggregate(context.Background(), req, parent); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("existing output not rejected: %v", err)
			}
			after, err := os.Lstat(final)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("existing output changed: %v", err)
			}
		})
	}
}

func TestAggregateCommonRootIsNotAModule(t *testing.T) {
	// /v1 is not a valid module major suffix, but is a valid common import
	// prefix for modules nested underneath it.
	if _, err := parseAggregateRequest("example.test/sdk/v1", "go-sdk", "1.0", "MIT", []string{"example.test/sdk/v1/a@v1.2.3"}); err != nil {
		t.Fatalf("common prefix incorrectly validated as a module: %v", err)
	}
}

func TestAggregateRequest(t *testing.T) {
	req, err := parseAggregateRequest("example.test/sdk", "go-example-sdk", "1.0~rc1", "MIT AND Apache-2.0", []string{
		"example.test/sdk/z/v2@v2.3.4", "example.test/sdk/a@v1.2.3",
	})
	if err != nil || len(req.Modules) != 2 || req.Modules[0].Path != "example.test/sdk/a" {
		t.Fatalf("request: %+v, %v", req, err)
	}
	cases := [][]string{
		nil, {"example.test/sdk/a"}, {"example.test/sdk/a@latest"},
		{"example.test/sdk/a@main"}, {"example.test/sdk/a@v1.2"},
		{"example.test/sdk/a@v1.2.3+custom"}, {"example.test/sdk/a@v2.0.0"},
		{"example.test/sdk-other/a@v1.2.3"}, {"example.test/sdk/a@v1.2.3", "example.test/sdk/a@v1.2.4"},
		{"example.test/sdk/a@v1.2.3", "example.test/sdk/a@v1.2.3"},
		{"example.test/sdk/a%{lua:bad}@v1.2.3"},
	}
	for _, args := range cases {
		if _, err := parseAggregateRequest("example.test/sdk", "go-sdk", "1.0", "MIT", args); err == nil {
			t.Errorf("accepted invalid arguments %q", args)
		}
	}
	for _, values := range [][4]string{
		{"example.test/sdk/../x", "go-sdk", "1", "MIT"},
		{"example.test/sdk", "../go-sdk", "1", "MIT"},
		{"example.test/sdk", "go-sdk\nSource0:evil", "1", "MIT"},
		{"example.test/sdk", "go-sdk", "1-2", "MIT"},
		{"example.test/sdk", "go-sdk", "1", "%{lua:bad}"},
		{"example.test/sdk", "go-sdk", "1", "MIT\nBuildRequires: evil"},
	} {
		if _, err := parseAggregateRequest(values[0], values[1], values[2], values[3], []string{"example.test/sdk/a@v1.2.3"}); err == nil {
			t.Errorf("accepted unsafe fields %q", values)
		}
	}
}

func TestAggregatePrepareRealGoFileProxy(t *testing.T) {
	req, _ := aggregateFixture(t)
	rawParent := t.TempDir()
	rawOutput, err := prepareAggregate(context.Background(), req, rawParent)
	if err != nil {
		t.Fatal(err)
	}
	rawEntries, err := os.ReadDir(rawOutput)
	if err != nil || len(rawEntries) != 1 || rawEntries[0].Name() != req.Name+".spec" {
		t.Fatalf("production output contains non-spec artifacts: %v", rawEntries)
	}
	parent := t.TempDir()
	output := prepareAggregateTestArtifacts(t, req, parent)
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != req.Name {
		t.Fatalf("temporary data leaked: %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(output, req.Name+".spec"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(data)
	for _, want := range []string{
		"%global go2spec_combine 1",
		"example.test/sdk/a 1.2.3", "example.test/sdk/b 0.2.0",
		"#!RemoteAsset:  sha256:", "Source0:        https://proxy.golang.org/", "Source1:        https://proxy.golang.org/",
		"go-rpm-macros >= 0.3", "%go_generate_provides", "export GOPROXY=off GOSUMDB=off",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(spec, "Provides:") || strings.Contains(spec, "TODO") {
		t.Fatal("generated a handwritten capability or incomplete draft")
	}
	for i, mod := range req.Modules {
		zipName := filepath.Join(output, fmt.Sprintf("module-%d-%s.zip", i, mod.Version))
		hash, err := fileSHA256(zipName)
		if err != nil || !strings.Contains(spec, hash) {
			t.Fatalf("Source checksum is not bound: %v", err)
		}
	}
	other := t.TempDir()
	second, err := prepareAggregate(context.Background(), req, other)
	if err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(second, req.Name+".spec"))
	if err != nil || !bytes.Equal(data, again) {
		t.Fatalf("fixed inputs produced different spec: %v", err)
	}
}

func TestAggregateFailureDoesNotPublish(t *testing.T) {
	req, _ := aggregateFixture(t)
	req.Modules[1].Version = "v0.3.0"
	parent := t.TempDir()
	if _, err := prepareAggregate(context.Background(), req, parent); err == nil {
		t.Fatal("missing second module succeeded")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial output left behind: %v, %v", entries, err)
	}
}

func TestAggregateExistingOutputPreserved(t *testing.T) {
	req, _ := aggregateFixture(t)
	parent := t.TempDir()
	marker := filepath.Join(parent, req.Name, "existing")
	aggregateWriteFile(t, marker, []byte("user data"))
	if _, err := prepareAggregate(context.Background(), req, parent); err == nil {
		t.Fatal("existing directory overwritten")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "user data" {
		t.Fatalf("existing data changed: %q, %v", data, err)
	}
}

func TestAggregateCLIRejectsConflictingFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-combine", "example.test/sdk", "-git_revision", "v1.0.0", "example.test/sdk/a@v1.2.3"},
		{"-combine", "example.test/sdk", "-list", "missing"},
		{"-combine", "example.test/sdk", "-type", "program", "example.test/sdk/a@v1.2.3"},
		{"-combine", ""}, {"-package_name", "go-sdk", "example.test/sdk/a@v1.2.3"},
		{"-combine", "example.test/sdk", "-force_prerelease=false", "example.test/sdk/a@v1.2.3"},
	} {
		if code := mainPack(args, nil); code == 0 {
			t.Errorf("CLI accepted %q", args)
		}
	}
}

func TestAggregateLayoutRejectsShadowing(t *testing.T) {
	root := t.TempDir()
	a := moduleSource{Module: module.Version{Path: "example.test/sdk/a"}, Dir: filepath.Join(root, "a")}
	b := moduleSource{Module: module.Version{Path: "example.test/sdk/a/nested"}, Dir: filepath.Join(root, "b")}
	aggregateWriteFile(t, filepath.Join(a.Dir, "nested", "unrelated.txt"), []byte("parent-owned"))
	aggregateWriteFile(t, filepath.Join(b.Dir, "value.go"), []byte("package nested\n"))
	plan := aggregatePlan{Sources: []moduleSource{a, b}}
	if err := validateAggregateLayout(plan); err == nil {
		t.Fatal("accepted parent source inside independently owned module")
	}
	if err := os.Remove(filepath.Join(a.Dir, "nested", "unrelated.txt")); err != nil {
		t.Fatal(err)
	}
	if err := validateAggregateLayout(plan); err != nil {
		t.Fatalf("empty parent directory rejected: %v", err)
	}
	aggregateWriteFile(t, filepath.Join(a.Dir, "other", "go.mod"), []byte("module example.test/sdk/a/other\n"))
	if err := validateAggregateLayout(plan); err == nil {
		t.Fatal("accepted unmapped nested module")
	}
}

func TestAggregatePackageDiscovery(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"root.go":               "package root\nimport _ \"example.other/runtime/deep\"\n",
		"root_test.go":          "package root\nimport _ \"example.other/test/deep\"\n",
		"child/child.go":        "package child\n",
		"examples/demo/demo.go": "package demo\n",
		"cmd/tool/main.go":      "package main\nfunc main(){}\n",
		"testdata/bad/bad.go":   "not Go",
		"windows.go":            "//go:build windows\n\npackage root\nimport _ \"example.other/windows\"\n",
	} {
		aggregateWriteFile(t, filepath.Join(dir, name), []byte(body))
	}
	plan := aggregatePlan{Request: aggregateRequest{Root: "example.test/sdk"}, Sources: []moduleSource{{Module: module.Version{Path: "example.test/sdk"}, Dir: dir}}}
	bc := build.Default
	bc.GOOS, bc.GOARCH = "linux", "amd64"
	packages, err := aggregatePackages(plan, bc)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 4 || !packages["example.test/sdk/child"].Library || packages["example.test/sdk/examples/demo"].Library || packages["example.test/sdk/cmd/tool"].Library {
		t.Fatalf("wrong package selection: %+v", packages)
	}
	rootPackage := packages["example.test/sdk"]
	if !reflect.DeepEqual(rootPackage.Imports, []string{"example.other/runtime/deep"}) || !reflect.DeepEqual(rootPackage.Tests, []string{"example.other/test/deep"}) {
		t.Fatalf("imports truncated or misclassified: %+v", rootPackage)
	}
}
