package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPkgsiteLicenseExpression(t *testing.T) {
	tests := []struct {
		name     string
		licenses []pkgsiteLicense
		want     string
	}{
		{
			name: "prefers top-level licenses",
			licenses: []pkgsiteLicense{
				{FilePath: "LICENSE", Types: []string{"MIT"}},
				{FilePath: "internal/LICENSE", Types: []string{"Apache-2.0"}},
			},
			want: "MIT",
		},
		{
			name: "joins separate license files with AND",
			licenses: []pkgsiteLicense{
				{FilePath: "LICENSE", Types: []string{"MIT"}},
				{FilePath: "COPYING", Types: []string{"BSD-3-Clause"}},
			},
			want: "BSD-3-Clause AND MIT",
		},
		{
			name: "joins multiple matches in one license file with OR",
			licenses: []pkgsiteLicense{
				{FilePath: "LICENSE", Types: []string{"MIT", "Apache-2.0"}},
			},
			want: "(Apache-2.0 OR MIT)",
		},
		{
			name:     "uses TODO when pkgsite has no SPDX type",
			licenses: []pkgsiteLicense{{FilePath: "LICENSE"}},
			want:     "TODO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pkgsiteLicenseExpression(tt.licenses); got != tt.want {
				t.Fatalf("pkgsiteLicenseExpression() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPkgsiteLicenseFilePaths(t *testing.T) {
	licenses := []pkgsiteLicense{
		{FilePath: "License", Types: []string{"MIT"}},
		{FilePath: "internal/LICENSE", Types: []string{"Apache-2.0"}},
		{FilePath: "License", Types: []string{"MIT"}},
	}
	got := strings.Join(pkgsiteLicenseFilePaths(licenses), ",")
	if want := "License"; got != want {
		t.Fatalf("pkgsiteLicenseFilePaths() = %q, want %q", got, want)
	}
}

func TestCleanSpecAssetPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "Readme", want: "Readme"},
		{in: "/docs/readme.md", want: "docs/readme.md"},
		{in: "../README.md", want: ""},
		{in: "docs/../README.md", want: ""},
	}
	for _, tt := range tests {
		if got := cleanSpecAssetPath(tt.in); got != tt.want {
			t.Fatalf("cleanSpecAssetPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWriteLicenseAndDocFilesOrdersDocBeforeSortedLicenses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "files")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	writeLicenseAndDocFiles(f, specAssetFiles{
		licenseFiles: []string{"zLICENSE", "COPYING", "LICENSE"},
		readmeFile:   "README.md",
	}, true)
	if err := f.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	want := "%doc README.md\n%license COPYING\n%license LICENSE\n%license zLICENSE\n"
	if got := string(content); got != want {
		t.Fatalf("writeLicenseAndDocFiles() = %q, want %q", got, want)
	}
}

func TestSummaryFromReadme(t *testing.T) {
	readme := `
# Project

[![Build Status](https://example.invalid/badge.svg)](https://example.invalid)
<p align="center">

This is the first useful line.
`
	if got, want := summaryFromReadme(readme), "This is the first useful line"; got != want {
		t.Fatalf("summaryFromReadme() = %q, want %q", got, want)
	}
}

func TestUnusableSummary(t *testing.T) {
	info := &pkgsiteInfo{
		Package: pkgsitePackage{Name: "runewidth"},
		Module:  pkgsiteModule{Path: "github.com/mattn/go-runewidth"},
	}
	if !unusableSummary("go-runewidth", "github.com/mattn/go-runewidth", info) {
		t.Fatalf("expected basename summary to be unusable")
	}
	if unusableSummary("Determines terminal display width", "github.com/mattn/go-runewidth", info) {
		t.Fatalf("expected descriptive summary to be usable")
	}
}

func TestCleanSummaryCandidate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "strips html", in: `<p align="center">Package termenv styles terminals.</p>`, want: "Styles terminals"},
		{name: "strips godoc package prefix", in: "Package isatty implements interface to isatty.", want: "Implements interface to isatty"},
		{name: "strips markdown image", in: `![testing](https://example.invalid) Package css parses CSS.`, want: "Parses CSS"},
		{name: "strips markdown link and code", in: "A [`Writer`](https://example.invalid) for ANSI output.", want: "A Writer for ANSI output"},
		{name: "uses first sentence", in: "Package termenv styles terminals. It supports colors.", want: "Styles terminals"},
		{name: "rejects html-only", in: `<p align="center">`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanSummaryCandidate(tt.in); got != tt.want {
				t.Fatalf("cleanSummaryCandidate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTarballURLForRef(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		ref     string
		want    string
	}{
		{
			name:    "github",
			repoURL: "https://github.com/google/go-cmp",
			ref:     "v0.7.0",
			want:    "https://github.com/google/go-cmp/archive/v0.7.0.tar.gz",
		},
		{
			name:    "gitlab subgroup",
			repoURL: "https://gitlab.com/group/subgroup/project",
			ref:     "v1.2.3",
			want:    "https://gitlab.com/group/subgroup/project/-/archive/v1.2.3/project-v1.2.3.tar.gz",
		},
		{
			name:    "sourcehut",
			repoURL: "https://git.sr.ht/~user/project",
			ref:     "v1.0.0",
			want:    "https://git.sr.ht/~user/project/archive/v1.0.0.tar.gz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := upstream{repoURL: tt.repoURL}
			got, err := u.tarballURLForRef(tt.ref, "gz")
			if err != nil {
				t.Fatalf("tarballURLForRef() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("tarballURLForRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitCloneURLFromRepoURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keeps github clone URL",
			in:   "https://github.com/google/go-cmp.git",
			want: "https://github.com/google/go-cmp",
		},
		{
			name: "converts Go source browser URL",
			in:   "https://cs.opensource.google/go/x/net",
			want: "https://go.googlesource.com/net",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gitCloneURLFromRepoURL(tt.in); got != tt.want {
				t.Fatalf("gitCloneURLFromRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGuessedPackageTypeUsesRootPackageName(t *testing.T) {
	tests := []struct {
		name string
		u    upstream
		want packageType
	}{
		{
			name: "library root with command subpackage stays library",
			u:    upstream{packageName: "yaml", firstMain: "go.yaml.in/yaml/v4/cmd/go-yaml"},
			want: typeLibrary,
		},
		{
			name: "library root without command subpackage stays library",
			u:    upstream{packageName: "yaml"},
			want: typeLibrary,
		},
		{
			name: "root main is program",
			u:    upstream{packageName: "main", firstMain: "example.com/tool"},
			want: typeProgram,
		},
		{
			name: "fallback to old main detection when pkgsite name missing",
			u:    upstream{firstMain: "example.com/tool"},
			want: typeProgram,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := guessedPackageType(&tt.u); got != tt.want {
				t.Fatalf("guessedPackageType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSourceImportPathForPackageUsesModuleRoot(t *testing.T) {
	info := &pkgsiteInfo{Package: pkgsitePackage{ModulePath: "github.com/example/project"}}
	if got, want := sourceImportPathForPackage("github.com/example/project/cmd/tool", info), "github.com/example/project"; got != want {
		t.Fatalf("sourceImportPathForPackage() = %q, want %q", got, want)
	}
	info = &pkgsiteInfo{Module: pkgsiteModule{Path: "github.com/example/module"}}
	if got, want := sourceImportPathForPackage("github.com/example/module/pkg", info), "github.com/example/module"; got != want {
		t.Fatalf("sourceImportPathForPackage(module fallback) = %q, want %q", got, want)
	}
	if got, want := sourceImportPathForPackage("example.com/no-module", nil), "example.com/no-module"; got != want {
		t.Fatalf("sourceImportPathForPackage(nil) = %q, want %q", got, want)
	}
}

func TestSetRepoDependenciesSeparatesRuntimeAndTestOnly(t *testing.T) {
	u := upstream{}
	u.setRepoDependencies(
		map[string]bool{
			"github.com/runtime/dep": true,
			"github.com/shared/dep":  true,
		},
		map[string]bool{
			"github.com/shared/dep":    true,
			"github.com/testonly/dep":  true,
			"github.com/testonly/dep2": true,
		},
	)

	if got, want := strings.Join(u.repoRunDeps, ","), "github.com/runtime/dep,github.com/shared/dep"; got != want {
		t.Fatalf("repoRunDeps = %q, want %q", got, want)
	}
	if got, want := strings.Join(u.repoTestDeps, ","), "github.com/testonly/dep,github.com/testonly/dep2"; got != want {
		t.Fatalf("repoTestDeps = %q, want %q", got, want)
	}
	if got, want := strings.Join(u.repoDeps, ","), "github.com/runtime/dep,github.com/shared/dep,github.com/testonly/dep,github.com/testonly/dep2"; got != want {
		t.Fatalf("repoDeps = %q, want %q", got, want)
	}
}

func TestModuleProxyEscapedPath(t *testing.T) {
	if got, want := moduleProxyEscapedPath("github.com/Azure/azure-sdk-for-go"), "github.com/!azure/azure-sdk-for-go"; got != want {
		t.Fatalf("moduleProxyEscapedPath() = %q, want %q", got, want)
	}
}

func TestModuleProxyVersionForSpec(t *testing.T) {
	if got, want := moduleProxyVersionForSpec("v1.0.0-RC1"), "v1.0.0-!r!c1"; got != want {
		t.Fatalf("moduleProxyVersionForSpec() = %q, want %q", got, want)
	}
	if got, want := moduleProxyVersionForSpec("v%{version}"), "v%{version}"; got != want {
		t.Fatalf("moduleProxyVersionForSpec() = %q, want %q", got, want)
	}
}

func TestModuleUsesRepoSubdir(t *testing.T) {
	tests := []struct {
		name       string
		modulePath string
		repoURL    string
		want       bool
	}{
		{
			name:       "real submodule",
			modulePath: "github.com/charmbracelet/x/ansi",
			repoURL:    "https://github.com/charmbracelet/x",
			want:       true,
		},
		{
			name:       "semantic import version suffix is not a repo subdir",
			modulePath: "github.com/aymanbagabas/go-osc52/v2",
			repoURL:    "https://github.com/aymanbagabas/go-osc52",
			want:       false,
		},
		{
			name:       "vanity module does not match repo host path",
			modulePath: "go.yaml.in/yaml/v4",
			repoURL:    "https://github.com/yaml/go-yaml",
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := moduleUsesRepoSubdir(tt.modulePath, tt.repoURL); got != tt.want {
				t.Fatalf("moduleUsesRepoSubdir() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetPkgsitePackageRetriesAmbiguousPathWithLongestModule(t *testing.T) {
	oldClient := pkgsiteHTTPClient
	defer func() { pkgsiteHTTPClient = oldClient }()

	var requested []string
	pkgsiteHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requested = append(requested, req.URL.RawQuery)
			if req.URL.Query().Get("module") == "" {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Status:     "400 Bad Request",
					Body: io.NopCloser(strings.NewReader(`{
						"message":"ambiguous package path",
						"candidates":[
							{"modulePath":"example.com/a","packagePath":"example.com/a/b/c"},
							{"modulePath":"example.com/a/b","packagePath":"example.com/a/b/c"}
						]
					}`)),
					Header: make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body: io.NopCloser(strings.NewReader(`{
					"modulePath":"example.com/a/b",
					"version":"v1.2.3",
					"path":"example.com/a/b/c",
					"name":"c"
				}`)),
				Header: make(http.Header),
			}, nil
		}),
	}

	p, err := getPkgsitePackage(t.Context(), "example.com/a/b/c", "")
	if err != nil {
		t.Fatalf("getPkgsitePackage() returned error: %v", err)
	}
	if p.ModulePath != "example.com/a/b" {
		t.Fatalf("ModulePath = %q, want %q", p.ModulePath, "example.com/a/b")
	}
	if len(requested) != 2 || requested[1] != "imports=true&licenses=true&module=example.com%2Fa%2Fb" {
		t.Fatalf("requests = %#v, want retry with longest module path", requested)
	}
}

func TestSourceURLForSpecUsesCommitIDMacro(t *testing.T) {
	u := upstream{
		repoURL: "https://github.com/example/project",
		version: "0+git20260522.abcdef\n%define commit_id 0123456789abcdef",
	}
	got, err := u.sourceURLForSpec("github.com/example/project")
	if err != nil {
		t.Fatalf("sourceURLForSpec() returned error: %v", err)
	}
	want := "https://github.com/example/project/archive/%{commit_id}.tar.gz#/%{_name}-%{version}.tar.gz"
	if got != want {
		t.Fatalf("sourceURLForSpec() = %q, want %q", got, want)
	}
}

func TestSourceURLForSpecKeepsVersionMacroForMatchingReleaseTag(t *testing.T) {
	u := upstream{
		repoURL:   "https://github.com/example/project",
		version:   "1.2.3",
		tag:       "v1.2.3",
		isRelease: true,
	}
	got, err := u.sourceURLForSpec("github.com/example/project")
	if err != nil {
		t.Fatalf("sourceURLForSpec() returned error: %v", err)
	}
	want := "https://github.com/example/project/archive/v%{version}.tar.gz#/%{_name}-%{version}.tar.gz"
	if got != want {
		t.Fatalf("sourceURLForSpec() = %q, want %q", got, want)
	}
}

func TestSourceURLForSpecFallsBackToModuleProxy(t *testing.T) {
	u := upstream{
		repoURL:   "https://go.googlesource.com/net",
		version:   "0.55.0",
		tag:       "v0.55.0",
		isRelease: true,
	}
	got, err := u.sourceURLForSpec("golang.org/x/net")
	if err != nil {
		t.Fatalf("sourceURLForSpec() returned error: %v", err)
	}
	want := "https://proxy.golang.org/golang.org/x/net/@v/v%{version}.zip#/%{_name}-%{version}.zip"
	if got != want {
		t.Fatalf("sourceURLForSpec() = %q, want %q", got, want)
	}
}

func TestSourceURLForSpecUsesModuleProxyForRepoSubmodule(t *testing.T) {
	u := upstream{
		repoURL:   "https://github.com/charmbracelet/x",
		version:   "0.1.0",
		tag:       "v0.1.0",
		isRelease: true,
	}
	got, err := u.sourceURLForSpec("github.com/charmbracelet/x/ansi")
	if err != nil {
		t.Fatalf("sourceURLForSpec() returned error: %v", err)
	}
	want := "https://proxy.golang.org/github.com/charmbracelet/x/ansi/@v/v%{version}.zip#/%{_name}-%{version}.zip"
	if got != want {
		t.Fatalf("sourceURLForSpec() = %q, want %q", got, want)
	}
}

func TestSourceURLForSpecRejectsCommitIDForRepoSubmodule(t *testing.T) {
	u := upstream{
		repoURL: "https://github.com/charmbracelet/x",
		version: "0+git20260522.abcdef\n%define commit_id abcdef1234567890",
	}
	_, err := u.sourceURLForSpec("github.com/charmbracelet/x/ansi")
	if err == nil {
		t.Fatalf("sourceURLForSpec() succeeded for commit-pinned submodule, want error")
	}
	if !strings.Contains(err.Error(), "canonical pseudo-version") {
		t.Fatalf("sourceURLForSpec() error = %v, want canonical pseudo-version message", err)
	}
}

func TestPkgVersionFromGitUsesPackagingDateAndSevenCharHash(t *testing.T) {
	oldNow := packagingDateNow
	packagingDateNow = func() time.Time {
		return time.Date(2025, 8, 8, 12, 0, 0, 0, time.UTC)
	}
	defer func() { packagingDateNow = oldNow }()

	dir := t.TempDir()
	runGit(t, dir, nil, "init")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/project\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	runGit(t, dir, nil, "add", "go.mod")
	runGit(t, dir, []string{
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_AUTHOR_DATE=1999-01-02T03:04:05Z",
		"GIT_COMMITTER_DATE=1999-01-02T03:04:05Z",
	}, "commit", "-m", "initial")

	fullHash := strings.TrimSpace(runGit(t, dir, nil, "rev-parse", "HEAD"))
	want := "0+git20250808." + fullHash[:7] + "\n%define commit_id " + fullHash

	u := upstream{}
	got, err := pkgVersionFromGit(dir, &u, "", false)
	if err != nil {
		t.Fatalf("pkgVersionFromGit() returned error: %v", err)
	}
	if got != want {
		t.Fatalf("pkgVersionFromGit() = %q, want %q", got, want)
	}
	if strings.Contains(got, "19990102") {
		t.Fatalf("pkgVersionFromGit() used commit date: %q", got)
	}
	if u.commitIsh != fullHash[:7] {
		t.Fatalf("commitIsh = %q, want %q", u.commitIsh, fullHash[:7])
	}
}

func runGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
