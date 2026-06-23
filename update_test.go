package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestUpdateSpecVersionPreservesFormatting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	original := strings.Join([]string{
		"Name:           golang-test",
		"Version:\t1.2.3  # keep-comment",
		"Release:        1%{?dist}",
		"%changelog",
		"%{?autochangelog}",
	}, "\n")
	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec := &specInfo{FilePath: specPath}
	if err := updateSpecVersion(spec, "2.0.0", strings.Repeat("a", 64), true); err != nil {
		t.Fatalf("updateSpecVersion: %v", err)
	}

	updated, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}

	got := string(updated)
	if !strings.Contains(got, "Version:\t2.0.0  # keep-comment") {
		t.Fatalf("version line not updated as expected:\n%s", got)
	}
	if !strings.Contains(got, "%autochangelog\n") {
		t.Fatalf("expected %%{{?autochangelog}} to be normalized, got:\n%s", got)
	}
	if strings.Contains(got, "%{?autochangelog}") {
		t.Fatalf("expected %%{{?autochangelog}} to be removed, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("expected rewritten spec to keep trailing newline, got %q", got)
	}
}

func TestUpdateSpecVersionReturnsErrorWhenVersionLineMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	if err := os.WriteFile(specPath, []byte("Name: golang-test\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec := &specInfo{FilePath: specPath}
	if err := updateSpecVersion(spec, "2.0.0", strings.Repeat("a", 64), true); err == nil {
		t.Fatal("expected updateSpecVersion to fail when Version line is missing")
	}
}

func TestParseSpecFileAcceptsGlobalMacros(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	original := strings.Join([]string{
		"%global go_import_path github.com/example/project/cmd/tool",
		"%global _name golang-github-example-project",
		"Version:        1.2.3",
		"Source0:        https://example.invalid/source.tar.gz",
	}, "\n")
	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec, err := parseSpecFile(specPath)
	if err != nil {
		t.Fatalf("parseSpecFile: %v", err)
	}
	if spec.GoImportPath != "github.com/example/project/cmd/tool" {
		t.Fatalf("GoImportPath = %q", spec.GoImportPath)
	}
	if spec.Name != "golang-github-example-project" {
		t.Fatalf("Name = %q", spec.Name)
	}
}

func TestDownloadCandidatesUseModulePathForPackageImport(t *testing.T) {
	t.Parallel()

	spec := &specInfo{
		GoImportPath:  "github.com/example/project/cmd/tool",
		LatestVersion: "v1.2.3",
	}
	info := &pkgsiteInfo{
		Package: pkgsitePackage{ModulePath: "github.com/example/project"},
		Module: pkgsiteModule{
			Path:    "github.com/example/project",
			RepoURL: "https://github.com/example/project",
		},
	}

	candidates, archiveErr, err := downloadCandidatesForSpec(spec, info)
	if err != nil {
		t.Fatalf("downloadCandidatesForSpec: %v", err)
	}
	if archiveErr != nil {
		t.Fatalf("archiveErr = %v", archiveErr)
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}
	if got, want := candidates[0].url, "https://github.com/example/project/archive/v1.2.3.tar.gz"; got != want {
		t.Fatalf("repo candidate = %q, want %q", got, want)
	}
	if got, want := candidates[1].url, "https://proxy.golang.org/github.com/example/project/@v/v1.2.3.zip"; got != want {
		t.Fatalf("proxy candidate = %q, want %q", got, want)
	}
}

func TestVerifyHTTPRequestFallsBackToRangeGet(t *testing.T) {
	t.Parallel()

	var sawHead atomic.Bool
	var sawGet atomic.Bool
	var gotRange atomic.Value
	gotRange.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			sawHead.Store(true)
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			sawGet.Store(true)
			gotRange.Store(r.Header.Get("Range"))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	if err := verifyHTTPRequest(context.Background(), srv.URL); err != nil {
		t.Fatalf("verifyHTTPRequest: %v", err)
	}
	if !sawHead.Load() {
		t.Fatal("expected HEAD request to be attempted first")
	}
	if !sawGet.Load() {
		t.Fatal("expected GET fallback to be attempted")
	}
	if gotRange.Load().(string) != "bytes=0-0" {
		t.Fatalf("unexpected Range header: %q", gotRange.Load().(string))
	}
}

func TestRepoRefForVersionStripsBuildMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "v2.74.0+incompatible", want: "v2.74.0"},
		{in: "2.74.0+incompatible", want: "v2.74.0"},
		{in: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{in: "release-2026-06-12", want: "vrelease-2026-06-12"},
	}

	for _, tc := range tests {
		if got := repoRefForVersion(tc.in); got != tc.want {
			t.Fatalf("repoRefForVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSourceVersionForSpecStripsBuildMetadata(t *testing.T) {
	t.Parallel()

	if got := sourceVersionForSpec("v2.74.0+incompatible"); got != "2.74.0" {
		t.Fatalf("sourceVersionForSpec() = %q, want %q", got, "2.74.0")
	}
}

func TestSpecVersionForVersionStripsIncompatible(t *testing.T) {
	t.Parallel()

	if got := specVersionForVersion("v2.74.0+incompatible"); got != "2.74.0" {
		t.Fatalf("specVersionForVersion() = %q, want %q", got, "2.74.0")
	}
}

func TestUpdateSpecVersionOnlyUpdatesSource0RemoteAsset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	source1Hash := strings.Repeat("b", 64)
	source0Hash := strings.Repeat("c", 64)
	original := strings.Join([]string{
		"Name:           golang-test",
		"Version:        1.0.0",
		"#!RemoteAsset:  sha256:" + source1Hash,
		"Source1:        https://example.invalid/extra.tar.gz",
		"Source0:        https://example.invalid/source.tar.gz",
	}, "\n")
	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec := &specInfo{FilePath: specPath}
	if err := updateSpecVersion(spec, "2.0.0", source0Hash, true); err != nil {
		t.Fatalf("updateSpecVersion: %v", err)
	}

	updated, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	got := string(updated)

	if !strings.Contains(got, "#!RemoteAsset:  sha256:"+source1Hash+"\nSource1:") {
		t.Fatalf("Source1 RemoteAsset was unexpectedly changed:\n%s", got)
	}
	if !strings.Contains(got, "#!RemoteAsset:  sha256:"+source0Hash+"\nSource0:") {
		t.Fatalf("Source0 RemoteAsset was not inserted correctly:\n%s", got)
	}
}

func TestUpdateSpecVersionClearsRemoteAssetWhenHashEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	sourceHash := strings.Repeat("d", 64)
	original := strings.Join([]string{
		"Name:           golang-test",
		"Version:        1.0.0",
		"#!RemoteAsset:  sha256:" + sourceHash,
		"Source0:        https://example.invalid/%{commit_id}.tar.gz",
	}, "\n")
	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec := &specInfo{FilePath: specPath}
	if err := updateSpecVersion(spec, "2.0.0", "", true); err != nil {
		t.Fatalf("updateSpecVersion: %v", err)
	}

	updated, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	got := string(updated)

	if !strings.Contains(got, "Version:        2.0.0") {
		t.Fatalf("version was not updated:\n%s", got)
	}
	if strings.Contains(got, "sha256:"+sourceHash) {
		t.Fatalf("RemoteAsset hash should be cleared:\n%s", got)
	}
	if !strings.Contains(got, "#!RemoteAsset\nSource0:") {
		t.Fatalf("RemoteAsset marker should be kept without hash:\n%s", got)
	}
}

func TestUpdateSpecVersionKeepsRemoteAssetWhenUpdateDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	sourceHash := strings.Repeat("d", 64)
	original := strings.Join([]string{
		"Name:           golang-test",
		"Version:        1.0.0",
		"#!RemoteAsset:  sha256:" + sourceHash,
		"Source0:        https://example.invalid/%{commit_id}.tar.gz",
	}, "\n")
	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec := &specInfo{FilePath: specPath}
	if err := updateSpecVersion(spec, "2.0.0", "", false); err != nil {
		t.Fatalf("updateSpecVersion: %v", err)
	}

	updated, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	got := string(updated)

	if !strings.Contains(got, "Version:        2.0.0") {
		t.Fatalf("version was not updated:\n%s", got)
	}
	if !strings.Contains(got, "#!RemoteAsset:  sha256:"+sourceHash) {
		t.Fatalf("RemoteAsset hash should be unchanged:\n%s", got)
	}
}

func TestResolvedSource0URLSkipsPinnedCommit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "test.spec")
	original := strings.Join([]string{
		"%define commit_id abcdef123456",
		"Name:           golang-test",
		"Version:        1.0.0",
		"Source0:        https://example.invalid/%{commit_id}.tar.gz",
	}, "\n")
	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	_, err := resolvedSource0URL(specPath, "2.0.0")
	if !errors.Is(err, errRemoteAssetSkip) {
		t.Fatalf("resolvedSource0URL error = %v, want errRemoteAssetSkip", err)
	}
}

func TestReleasePageURLForSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source  string
		wantURL string
		wantTag string
	}{
		{
			source:  "https://github.com/apache/beam/archive/refs/tags/v2.74.0.tar.gz",
			wantURL: "https://github.com/apache/beam/releases/tag/v2.74.0",
			wantTag: "v2.74.0",
		},
		{
			source:  "https://github.com/charmbracelet/x/archive/refs/tags/term/v0.2.2.tar.gz",
			wantURL: "https://github.com/charmbracelet/x/releases/tag/term/v0.2.2",
			wantTag: "term/v0.2.2",
		},
		{
			source:  "https://gitlab.com/group/project/-/archive/v1.2.3/project-v1.2.3.tar.gz",
			wantURL: "https://gitlab.com/group/project/-/releases/v1.2.3",
			wantTag: "v1.2.3",
		},
	}

	for _, tc := range tests {
		gotURL, gotTag := releasePageURLForSource(tc.source)
		if gotURL != tc.wantURL || gotTag != tc.wantTag {
			t.Fatalf("releasePageURLForSource(%q) = (%q, %q), want (%q, %q)",
				tc.source, gotURL, gotTag, tc.wantURL, tc.wantTag)
		}
	}
}

func TestExtractSHA256FromHTML(t *testing.T) {
	t.Parallel()

	want := strings.Repeat("a", 64)
	html := "Release notes\nSHA256: " + want
	if got := extractSHA256FromHTML(html, "v1.2.3"); got != want {
		t.Fatalf("extractSHA256FromHTML() = %q, want %q", got, want)
	}
}

func TestDownloadAssetSHA256(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("source archive"))
	}))
	defer srv.Close()

	got, err := downloadAssetSHA256(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("downloadAssetSHA256: %v", err)
	}
	want := "6ad189ace456a83fade855d5a647cd8ad9e7966da4404b1187218dca3d9eddaa"
	if got != want {
		t.Fatalf("downloadAssetSHA256() = %q, want %q", got, want)
	}
}

func TestVerifyDownloadCandidatesFallsBackToProxy(t *testing.T) {
	t.Parallel()

	var logsMu sync.Mutex
	var logs []string
	oldLog := downloadRetryLog
	downloadRetryLog = func(msg string) {
		logsMu.Lock()
		defer logsMu.Unlock()
		logs = append(logs, msg)
	}
	defer func() { downloadRetryLog = oldLog }()

	repoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer repoSrv.Close()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer proxySrv.Close()

	err := verifyDownloadCandidates(context.Background(), "codeberg.org/go-fonts/liberation", []downloadCandidate{
		{source: "repo", url: repoSrv.URL},
		{source: "proxy", url: proxySrv.URL},
	})
	if err != nil {
		t.Fatalf("verifyDownloadCandidates: %v", err)
	}

	logsMu.Lock()
	defer logsMu.Unlock()
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "trying proxy") {
		t.Fatalf("expected fallback log, got: %s", joined)
	}
	if !strings.Contains(joined, "verified via proxy after fallback") {
		t.Fatalf("expected fallback success log, got: %s", joined)
	}
}

func TestIsRetryableDownloadErrorRecognizesTimeout(t *testing.T) {
	t.Parallel()

	timeoutErr := &net.DNSError{IsTimeout: true}
	if !isRetryableDownloadError(timeoutErr) {
		t.Fatal("expected timeout net error to be retryable")
	}
	if !isRetryableDownloadError(errors.New("HTTP 504 from example")) {
		t.Fatal("expected HTTP 504 to be retryable")
	}
	if isRetryableDownloadError(fmt.Errorf("HTTP 404 from example")) {
		t.Fatal("did not expect HTTP 404 to be retryable")
	}
}
