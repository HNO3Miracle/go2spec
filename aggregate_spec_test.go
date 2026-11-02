package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func aggregateSpecSection(t *testing.T, spec, start, end string) string {
	t.Helper()
	_, body, ok := strings.Cut(spec, "\n%"+start+"\n")
	if !ok {
		t.Fatalf("missing %%%s section", start)
	}
	body, _, ok = strings.Cut(body, "\n%"+end+"\n")
	if !ok {
		t.Fatalf("missing %%%s boundary", end)
	}
	return body
}

// This exercises the generated shell with real unzip, SHA256 and Go commands.
// It is a source-layout test, not an RPM/installed-macro/header acceptance test.
func TestAggregateSpecOfflineSourceLayout(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(strconv.Itoa(count)+"-modules", func(t *testing.T) {
			testAggregateSpecOfflineSourceLayout(t, count)
		})
	}
}

func testAggregateSpecOfflineSourceLayout(t *testing.T, count int) {
	t.Helper()
	req, proxy := aggregateFixture(t)
	args := []string{"example.test/sdk/a@v1.2.3", "example.test/sdk/b@v0.2.0"}
	if count == 3 {
		args = append(args, "example.test/sdk/c/v2@v2.3.4")
	}
	var err error
	req, err = parseAggregateRequest(req.Root, req.Name, req.Version, req.License, args)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := make(map[string]string)
	wantLicenses := make(map[string]string)
	for i, mod := range req.Modules {
		files := map[string]string{
			"go.mod":            "module " + mod.Path + "\n\ngo 1.18\n",
			"LICENSE":           fmt.Sprintf("License for %s\n", mod.Path),
			"NOTICE":            fmt.Sprintf("Notice for %s\n", mod.Path),
			"legal/LICENSE.txt": fmt.Sprintf("Nested license for %s\n", mod.Path),
			"value.go":          fmt.Sprintf("package value\nfunc Value() int { return %d }\n", i+1),
			"value_test.go":     fmt.Sprintf("package value\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != %d { t.Fatal(Value()) } }\n", i+1),
		}
		aggregateProxyModule(t, proxy, mod, files)
		for name, data := range files {
			wantSources[mod.Path+"/"+name] = data
		}
		for _, name := range []string{"LICENSE", "NOTICE", "legal/LICENSE.txt"} {
			wantLicenses[fmt.Sprintf("module-%d/%s", i, name)] = files[name]
		}
	}
	output := prepareAggregateTestArtifacts(t, req, t.TempDir())
	data, err := os.ReadFile(filepath.Join(output, req.Name+".spec"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(data)
	if strings.Count(spec, "%go_generate_provides ") != 1 || strings.Contains(spec, "Provides:") || strings.Contains(spec, "go_submodules_file") {
		t.Fatal("wrong auto-Provides interface")
	}
	if strings.Contains(spec, "ver_module") || strings.Contains(spec, "sha256sum -c") {
		t.Fatal("redundant version macros or build-time checksum commands")
	}
	for _, mod := range req.Modules {
		if !strings.Contains(spec, mod.Path+" "+strings.TrimPrefix(mod.Version, "v")+"\n") {
			t.Fatalf("missing independent version row for %s", mod.Path)
		}
	}
	slices.Reverse(args)
	reordered, err := parseAggregateRequest(req.Root, req.Name, req.Version, req.License, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareAggregate(context.Background(), reordered, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(second, req.Name+".spec"))
	if err != nil || string(again) != spec {
		t.Fatalf("argument order changed generated spec: %v", err)
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	layout, buildroot := filepath.Join(t.TempDir(), "layout with spaces"), t.TempDir()
	if err := os.Mkdir(layout, 0755); err != nil {
		t.Fatal(err)
	}
	replacements := []string{
		"%setup -q -c -T", ": # layout directory already exists",
		"%{go_import_path}", req.Root, "%{version}", req.Version,
		"%{buildroot}", buildroot, "%{go_sys_gopath}", "/usr/share/gocode/src",
		"%{gopath}", t.TempDir(), "%{__go}", strconv.Quote(goCommand), "%{go_test_flags_default}", "-v",
	}
	// Source paths may contain spaces or quotes.
	sources := filepath.Join(t.TempDir(), "Sources with spaces ' quoted")
	if err := os.Mkdir(sources, 0755); err != nil {
		t.Fatal(err)
	}
	for i, mod := range req.Modules {
		name := fmt.Sprintf("module-%d-%s.zip", i, mod.Version)
		zip, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		aggregateWriteFile(t, filepath.Join(sources, name), zip)
		replacements = append(replacements, "%{SOURCE"+strconv.Itoa(i)+"}", filepath.Join(sources, name))
	}
	expand := strings.NewReplacer(replacements...)
	run := func(dir, body string) ([]byte, error) {
		cmd := exec.Command("bash", "-eu", "-c", expand.Replace(body))
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	prep := aggregateSpecSection(t, spec, "prep", "build")
	if out, err := run(layout, prep); err != nil {
		t.Fatalf("prep: %v\n%s", err, out)
	}
	install := aggregateSpecSection(t, spec, "install", "check")
	// The macro itself must be validated in openRuyi, not replaced with a
	// mock that claims to verify its marker/payload/header behavior.
	install = strings.Replace(install, "%go_generate_provides -i %{go_import_path} -v %{version}\n", "", 1)
	if out, err := run(layout, install); err != nil {
		t.Fatalf("install layout: %v\n%s", err, out)
	}
	aggregateAssertTree(t, filepath.Join(layout, "gopath", "src"), wantSources)
	aggregateAssertTree(t, filepath.Join(buildroot, "usr/share/gocode/src"), wantSources)
	aggregateAssertTree(t, filepath.Join(layout, "licenses"), wantLicenses)
	if out, err := run(layout, aggregateSpecSection(t, spec, "check", "files")); err != nil {
		t.Fatalf("offline Go check: %v\n%s", err, out)
	} else if strings.Count(string(out), "--- PASS: TestValue") != count {
		t.Fatalf("not all modules' tests ran:\n%s", out)
	}
}

func aggregateAssertTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	wantFiles, wantDirs := make(map[string]string), map[string]bool{".": true}
	for name, data := range files {
		wantFiles[name] = data
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			wantDirs[filepath.ToSlash(dir)] = true
		}
	}
	gotFiles, gotDirs := make(map[string]string), make(map[string]bool)
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			gotDirs[rel] = true
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular tree entry: %s", name)
		}
		data, err := os.ReadFile(name)
		gotFiles[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFiles, wantFiles) || !reflect.DeepEqual(gotDirs, wantDirs) {
		t.Fatalf("tree mismatch at %s\nfiles: got %q, want %q\ndirs: got %v, want %v", root, gotFiles, wantFiles, gotDirs, wantDirs)
	}
}

func TestAggregateSpecRejectsUnsafeSourceFields(t *testing.T) {
	req, _ := aggregateFixture(t)
	work := t.TempDir()
	t.Cleanup(func() { _ = removeAggregateWork(work) })
	plan, err := buildAggregatePlan(context.Background(), req, work)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"filename", "hash", "license", "source-url"} {
		t.Run(field, func(t *testing.T) {
			bad := plan
			bad.Sources = append([]moduleSource(nil), plan.Sources...)
			switch field {
			case "filename":
				bad.Sources[0].FileName = "../other.zip"
			case "hash":
				bad.Sources[0].SHA256 = "not-a-file-checksum"
			case "license":
				bad.Sources[0].LicenseFiles = []string{"../../outside"}
			case "source-url":
				bad.Sources[0].SourceURL = "https://user:secret@example.test/source.zip#/module.zip"
			}
			if _, err := renderAggregateSpec(bad); err == nil {
				t.Fatal("unsafe source field accepted")
			}
		})
	}
	dir := t.TempDir()
	if err := writeAggregateSpec(dir, plan); err != nil {
		t.Fatal(err)
	}
	if err := writeAggregateSpec(dir, plan); err == nil {
		t.Fatal("existing spec overwritten")
	}
}

func TestAggregateNeutralModuleAndCleanup(t *testing.T) {
	req, _ := aggregateFixture(t)
	parent := t.TempDir()
	aggregateWriteFile(t, filepath.Join(parent, "go.mod"), []byte("module unrelated.test/consumer\n\ngo 99.0.0\n"))
	t.Setenv("GOWORK", filepath.Join(parent, "absent.go.work"))
	if _, err := prepareAggregate(context.Background(), req, parent); err != nil {
		t.Fatalf("inherited caller's module/workspace: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 2 {
		t.Fatalf("private cache or work files remain: %v, %v", entries, err)
	}
	work, outside := t.TempDir(), t.TempDir()
	aggregateWriteFile(t, filepath.Join(work, "cache", "nested", "data"), []byte("owned"))
	aggregateWriteFile(t, filepath.Join(outside, "data"), []byte("not ours"))
	if err := os.Chmod(filepath.Join(work, "cache", "nested"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work, "cache"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "external")); err != nil {
		t.Fatal(err)
	}
	if err := removeAggregateWork(work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("read-only cache was not removed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "data")); err != nil || string(data) != "not ours" {
		t.Fatalf("cleanup crossed the work boundary: %q, %v", data, err)
	}
}
