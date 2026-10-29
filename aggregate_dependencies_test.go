package main

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func TestAggregateDependenciesPreserveImportPaths(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "source")
	for name, body := range map[string]string{
		"a.go": `package a
import (
 _ "fmt"
 _ "example.test/sdk/a/split/v2/client"
 _ "example.test/sdk/a/examples/helper"
 _ "example.test/sdk/a/internal/helper"
 _ "example.test/sdk/b/child"
 _ "example.other/runtime/deep/package"
)`,
		"a_test.go":                 "package a\nimport _ \"example.other/test/deep/package\"\n",
		"internal/helper/helper.go": "package helper\n",
		"examples/helper/helper.go": "package helper\nimport _ \"example.other/helper/runtime\"\n",
		"examples/demo/demo.go":     "package demo\nimport _ \"example.other/demo/only\"\n",
		"cmd/tool/main.go":          "package main\nimport _ \"example.other/command/only\"\nfunc main(){}\n",
	} {
		aggregateWriteFile(t, filepath.Join(dir, name), []byte(body))
	}
	bDir := filepath.Join(work, "other-source")
	aggregateWriteFile(t, filepath.Join(bDir, "child", "child.go"), []byte("package child\n"))
	plan := aggregatePlan{Request: aggregateRequest{Root: "example.test/sdk"}, GoVersion: "1.18", Sources: []moduleSource{
		{Module: module.Version{Path: "example.test/sdk/a"}, Dir: dir,
			Requires: []module.Version{{Path: "example.test/sdk/a/split/v2", Version: "v2.0.0"}}},
		{Module: module.Version{Path: "example.test/sdk/b"}, Dir: bDir},
	}}
	if err := aggregateDependencies(context.Background(), work, &plan); err != nil {
		t.Fatal(err)
	}
	wantRun := []string{"example.other/helper/runtime", "example.other/runtime/deep/package", "example.test/sdk/a/split/v2/client"}
	wantBuild := append(slices.Clone(wantRun), "example.other/test/deep/package", "example.other/command/only", "example.other/demo/only")
	slices.Sort(wantBuild)
	if !reflect.DeepEqual(plan.BuildDeps, wantBuild) || !reflect.DeepEqual(plan.RunDeps, wantRun) {
		t.Fatalf("build=%q runtime=%q; want build=%q runtime=%q", plan.BuildDeps, plan.RunDeps, wantBuild, wantRun)
	}
}

func TestAggregateDependencyDiscoveryRejectsIncompleteInput(t *testing.T) {
	for _, kind := range []string{"missing-internal", "main-import", "test-only-import", "cgo", "mixed-packages", "relative-import", "unknown-standard", "standard-internal", "foreign-internal", "future-toolchain"} {
		t.Run(kind, func(t *testing.T) {
			work := t.TempDir()
			dir := filepath.Join(work, "source")
			files := map[string]string{"a.go": "package a\n"}
			minimum := "1.18"
			switch kind {
			case "missing-internal":
				files["a.go"] += "import _ \"example.test/sdk/a/missing\"\n"
			case "main-import":
				files["a.go"] += "import _ \"example.test/sdk/a/cmd\"\n"
				files["cmd/main.go"] = "package main\nfunc main(){}\n"
			case "test-only-import":
				files["a.go"] += "import _ \"example.test/sdk/a/child\"\n"
				files["child/child_test.go"] = "package child\n"
			case "cgo":
				files["a.go"] += "import \"C\"\n"
			case "mixed-packages":
				files["b.go"] = "package different\n"
			case "relative-import":
				files["a.go"] += "import _ \"../other\"\n"
			case "unknown-standard":
				files["a.go"] += "import _ \"missingstandard\"\n"
			case "standard-internal":
				files["a.go"] += "import _ \"internal/testenv\"\n"
			case "foreign-internal":
				files["a.go"] += "import _ \"example.other/private/internal/hidden\"\n"
			case "future-toolchain":
				minimum = "99.0.0"
			}
			for name, body := range files {
				aggregateWriteFile(t, filepath.Join(dir, name), []byte(body))
			}
			plan := aggregatePlan{Request: aggregateRequest{Root: "example.test/sdk"}, GoVersion: minimum,
				Sources: []moduleSource{{Module: module.Version{Path: "example.test/sdk/a"}, Dir: dir}}}
			if err := aggregateDependencies(context.Background(), work, &plan); err == nil {
				t.Fatal("incomplete dependency discovery succeeded")
			}
		})
	}
}

func TestAggregateDependenciesRejectArchitectureDifference(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "source")
	aggregateWriteFile(t, filepath.Join(dir, "common.go"), []byte("package p\n"))
	aggregateWriteFile(t, filepath.Join(dir, "riscv.go"), []byte("//go:build riscv64\n\npackage p\nimport _ \"example.other/riscv-only\"\n"))
	plan := aggregatePlan{Request: aggregateRequest{Root: "example.test/sdk"}, GoVersion: "1.18", Sources: []moduleSource{{Module: module.Version{Path: "example.test/sdk"}, Dir: dir}}}
	if err := aggregateDependencies(context.Background(), work, &plan); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("architecture-dependent dependency set was accepted: %v", err)
	}
}
func TestAggregateInstalledToolchainContext(t *testing.T) {
	work := t.TempDir()
	t.Setenv("GOFLAGS", "-tags=unrelated")
	t.Setenv("GOARCH", "wasm")
	t.Setenv("GOOS", "js")
	for _, arch := range []string{"amd64", "riscv64"} {
		bc, std, err := aggregateBuildContext(context.Background(), work, arch)
		if err != nil {
			t.Fatal(err)
		}
		if bc.GOOS != "linux" || bc.GOARCH != arch || !bc.CgoEnabled || len(bc.BuildTags) != 0 || !std["fmt"] || !std["unsafe"] || !slices.Contains(bc.ReleaseTags, "go1.18") {
			t.Fatalf("wrong target context: %+v", bc)
		}
		feature := "amd64.v1"
		if arch == "riscv64" {
			feature = "riscv64.rva20u64"
		}
		if !slices.Contains(bc.ToolTags, feature) {
			t.Fatalf("missing architecture tag %s: %q", feature, bc.ToolTags)
		}
		// Exercise actual tool tags returned by Go, including its default
		// experiments, rather than assuming the binary's build.Default tags.
		dir := filepath.Join(work, arch)
		for _, tag := range bc.ToolTags {
			name := strings.ReplaceAll(tag, ".", "_") + ".go"
			aggregateWriteFile(t, filepath.Join(dir, name), []byte("//go:build "+tag+"\n\npackage tagged\nimport _ \"example.other/"+tag+"\"\n"))
		}
		plan := aggregatePlan{Request: aggregateRequest{Root: "example.test/sdk"}, Sources: []moduleSource{{Module: module.Version{Path: "example.test/sdk"}, Dir: dir}}}
		packages, err := aggregatePackages(plan, bc)
		if err != nil || len(packages["example.test/sdk"].Imports) != len(bc.ToolTags) {
			t.Fatalf("tool-tag imports were dropped: %+v, %v", packages, err)
		}
	}
}

func TestAggregateSelectedOwnershipUsesModuleBoundaries(t *testing.T) {
	plan := aggregatePlan{Sources: []moduleSource{
		{Module: module.Version{Path: "example.test/sdk"}, Requires: []module.Version{{Path: "example.test/sdk/sub"}}},
		{Module: module.Version{Path: "example.test/sdk/sub/selected"}},
	}}
	for dep, want := range map[string]bool{
		"example.test/sdk/root":                true,
		"example.test/sdk/sub/client":          false,
		"example.test/sdk/submarine/client":    true,
		"example.test/sdk/sub/selected/client": true,
		"example.test/other":                   false,
	} {
		if got := aggregateOwnsImport(plan, dep); got != want {
			t.Errorf("ownership of %s = %t, want %t", dep, got, want)
		}
	}
}
