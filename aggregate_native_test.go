package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

// The normal suite always generates and tests this fixture. Setting the output
// directory exports the same inputs for the separate native RPM acceptance test;
// it does not skip a test or claim that any RPM has been built.
func TestAggregateNativeFixture(t *testing.T) {
	license, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	const root = "example.test/go2spec/fixture"
	const name = "go-go2spec-combine-fixture"
	mods := []module.Version{{Path: root, Version: "v1.2.3"}, {Path: root + "/sub", Version: "v0.4.0"}}
	files := []map[string]string{
		{
			"go.mod":                 "module " + root + "\n\ngo 1.18\nrequire " + root + "/sub v0.4.0\n",
			"base/base.go":           "package base\nimport \"" + root + "/sub\"\nfunc Version() string { return \"1.2.3:\" + sub.Version() }\n",
			"base/base_test.go":      "package base\nimport \"testing\"\nfunc TestBaseVersion(t *testing.T) { if Version() != \"1.2.3:0.4.0\" { t.Fatal(Version()) } }\n",
			"submarine/submarine.go": "package submarine\nfunc Version() string { return \"1.2.3\" }\n",
			"cmd/demo/main.go":       "package main\nimport (\"fmt\"; \"" + root + "/base\")\nfunc main() { fmt.Println(base.Version()) }\n",
			"examples/demo/demo.go":  "package demo\nfunc Example() string { return \"not a provided package\" }\n",
			"testonly/only_test.go":  "package testonly\nimport \"testing\"\nfunc TestOnlyPackage(t *testing.T) {}\n",
		},
		{
			"go.mod":            "module " + root + "/sub\n\ngo 1.18\n",
			"sub.go":            "package sub\nfunc Version() string { return \"0.4.0\" }\n",
			"sub_test.go":       "package sub\nimport \"testing\"\nfunc TestSubVersion(t *testing.T) { if Version() != \"0.4.0\" { t.Fatal(Version()) } }\n",
			"leaf/leaf.go":      "package leaf\nimport \"" + root + "/sub\"\nfunc Version() string { return sub.Version() }\n",
			"leaf/leaf_test.go": "package leaf\nimport \"testing\"\nfunc TestLeafVersion(t *testing.T) { if Version() != \"0.4.0\" { t.Fatal(Version()) } }\n",
		},
	}
	proxy := t.TempDir()
	for i, mod := range mods {
		files[i]["LICENSE"] = string(license)
		aggregateProxyModule(t, proxy, mod, files[i])
	}
	aggregateProxyEnv(t, proxy)

	output := os.Getenv("GO2SPEC_NATIVE_FIXTURE_DIR")
	if output == "" {
		output = t.TempDir()
	} else {
		if !filepath.IsAbs(output) {
			t.Fatal("GO2SPEC_NATIVE_FIXTURE_DIR must be an absolute, new directory")
		}
		// Never merge test output into an existing user directory, even if empty.
		if err := os.Mkdir(output, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(output)
	if code := mainPack([]string{
		"-combine", root, "-package_name", name, "-package_version", "99.0", "-url", "https://example.test/go2spec/fixture",
		"-license", "BSD-3-Clause", mods[1].Path + "@" + mods[1].Version, mods[0].Path + "@" + mods[0].Version,
	}, nil); code != 0 {
		t.Fatalf("fixture pack exit=%d", code)
	}
	packageDir := filepath.Join(output, name)
	req, err := parseAggregateRequest(root, name, "99.0", "BSD-3-Clause", []string{mods[1].Path + "@" + mods[1].Version, mods[0].Path + "@" + mods[0].Version})
	if err != nil {
		t.Fatal(err)
	}
	req.URL = "https://example.test/go2spec/fixture"
	planWork := t.TempDir()
	t.Cleanup(func() { _ = removeAggregateWork(planWork) })
	plan, err := buildAggregatePlan(context.Background(), req, planWork)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range plan.Sources {
		if err := copyFile(source.Zip, filepath.Join(packageDir, source.FileName)); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := os.ReadFile(filepath.Join(packageDir, name+".spec"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{root + " 1.2.3\n", root + "/sub 0.4.0\n", "URL:            https://example.test/go2spec/fixture\n", "Source0:        https://proxy.golang.org/"} {
		if !strings.Contains(string(spec), want) {
			t.Fatalf("fixture lost independent version mapping %q", want)
		}
	}
	if strings.Contains(string(spec), "Provides:") || strings.Contains(string(spec), "BuildRequires:  go(") {
		t.Fatal("fixture acquired handwritten Provides or unexpected external imports")
	}

	stage := t.TempDir()
	for i, mod := range mods {
		zipName := fmt.Sprintf("module-%d-%s.zip", i, mod.Version)
		if err := modzip.Unzip(filepath.Join(stage, "src", filepath.FromSlash(mod.Path)), mod, filepath.Join(packageDir, zipName)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", root+"/...")
	cmd.Dir = stage
	// os/exec uses the last occurrence of a duplicate environment key.
	cmd.Env = append(combinedGoEnv(stage), "GO111MODULE=off", "GOPATH="+stage, "GOPROXY=off", "GOSUMDB=off")
	testOutput, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture's own offline Go tests: %v\n%s", err, testOutput)
	}
	testNames := []string{"TestBaseVersion", "TestSubVersion", "TestLeafVersion", "TestOnlyPackage"}
	for _, test := range testNames {
		if !bytes.Contains(testOutput, []byte("--- PASS: "+test+" (")) {
			t.Fatalf("fixture did not execute %s:\n%s", test, testOutput)
		}
	}

	// These are expectations for the native verifier, not synthetic markers.
	provides := "go(" + root + "/base) = 1.2.3\n" +
		"go(" + root + "/sub) = 0.4.0\n" +
		"go(" + root + "/sub/leaf) = 0.4.0\n" +
		"go(" + root + "/submarine) = 1.2.3\n"
	aggregateWriteFile(t, filepath.Join(output, "expected-provides.txt"), []byte(provides))
	aggregateWriteFile(t, filepath.Join(output, "expected-tests.txt"), []byte(strings.Join(testNames, "\n")+"\n"))
	aggregateWriteFile(t, filepath.Join(output, "fixture-go-tests.log"), testOutput)
	t.Logf("native RPM fixture prepared at %s; native build still requires tests/native-combine.sh", output)
}

func TestNativeCombineScriptRefusesExistingWork(t *testing.T) {
	script, err := filepath.Abs("tests/native-combine.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"directory", "file", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			work, target := filepath.Join(root, "work"), filepath.Join(root, "absent-target")
			switch kind {
			case "directory":
				err = os.Mkdir(work, 0755)
			case "file":
				err = os.WriteFile(work, []byte("existing data"), 0644)
			case "dangling-symlink":
				err = os.Symlink(target, work)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(work)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", script, filepath.Join(root, "missing-fixture"), work)
			out, err := cmd.CombinedOutput()
			if err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 1 || !bytes.Contains(out, []byte("build directory already exists")) {
				t.Fatalf("existing work was not refused before other operations: %v\n%s", err, out)
			}
			after, err := os.Lstat(work)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("existing work changed: %v", err)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("followed dangling symlink and created its target: %v", err)
			}
		})
	}
}
