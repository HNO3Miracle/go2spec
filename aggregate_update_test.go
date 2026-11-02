package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const aggregateUpdateOrdinarySpec = `%global go_import_path example.com/ordinary
%global _name ordinary
Name: go-ordinary
Version: 1.0.0
Release: 1
Source0: https://assets.invalid/ordinary-v%{version}.tar.gz
%description
Ordinary fixture.
%files
`

func aggregateUpdateFixture(t *testing.T, dir, text string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture.spec")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func aggregateUpdateUnchanged(t *testing.T, path, original string, before os.FileInfo) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("spec content, inode or modification time changed: %s", path)
	}
}

func aggregateUpdateStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func aggregateUpdateWantUnsupported(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "go2spec_combine") {
		t.Errorf("error = %v, want unsupported go2spec_combine", err)
	}
}

// No test using this helper makes a socket request. It covers both the pkgsite
// client and clients constructed inside the updater (including mainUpdate's
// cache transport, whose fallback is http.DefaultTransport).
func aggregateUpdateTransport(t *testing.T, respond func(*http.Request) *http.Response) *atomic.Int64 {
	t.Helper()
	oldTransport, oldClient := http.DefaultTransport, pkgsiteHTTPClient
	oldBase, oldCache := pkgsiteAPIBase, pkgsiteInfoCache
	oldPkgLog, oldDownloadLog := pkgsiteRetryLog, downloadRetryLog
	calls := new(atomic.Int64)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if respond != nil {
			return respond(req), nil
		}
		return aggregateUpdateResponse(req, http.StatusForbidden, `{"message":"network forbidden by aggregate-update test"}`), nil
	})
	http.DefaultTransport = transport
	pkgsiteHTTPClient = &http.Client{Transport: transport}
	pkgsiteAPIBase = "https://pkgsite.invalid/v1beta"
	pkgsiteInfoCache = make(map[string]*pkgsiteInfo)
	pkgsiteRetryLog, downloadRetryLog = nil, nil
	t.Cleanup(func() {
		http.DefaultTransport, pkgsiteHTTPClient = oldTransport, oldClient
		pkgsiteAPIBase, pkgsiteInfoCache = oldBase, oldCache
		pkgsiteRetryLog, downloadRetryLog = oldPkgLog, oldDownloadLog
	})
	return calls
}

func aggregateUpdateResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}
}

func aggregateUpdateCLI(t *testing.T, args []string) (int, string) {
	t.Helper()
	capture, err := os.CreateTemp(t.TempDir(), "cli-output-")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	code := mainUpdate(args)
	if _, err := capture.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(capture)
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data)
}

func TestAggregateUpdateRejectsLiteralDefinitions(t *testing.T) {
	markers := []string{
		"%global go2spec_combine 1",
		"%define go2spec_combine 1",
		"  %global\tgo2spec_combine\t0\r",
		"%global go2spec_combine",
		"%define go2spec_combine %{lua:error('must not execute')}",
		"%global go2spec_combine %(must-not-execute)",
		"%define go2spec_combine() 1",
		"%define go2spec_combine(x) 0",
		"%if 0\n%global go2spec_combine 1\n%endif",
		"# " + strings.Repeat("x", 70000) + "\n%global go2spec_combine 1",
	}
	for i, marker := range markers {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			// Put definitions after Source0 to catch scanners that return early.
			text := aggregateUpdateOrdinarySpec + marker + "\n"
			path := aggregateUpdateFixture(t, t.TempDir(), text)
			before := aggregateUpdateStat(t, path)
			_, err := parseSpecFile(path)
			aggregateUpdateWantUnsupported(t, err)
			aggregateUpdateUnchanged(t, path, text, before)
		})
	}
}

func TestAggregateUpdateIgnoresCommentsAndOtherNames(t *testing.T) {
	lines := []string{
		"# %global go2spec_combine 1",
		" \t# %define go2spec_combine 0",
		"Summary: documentation mentions %global go2spec_combine 1",
		"%global go2spec_combine_extra 1",
		"%define go2spec_combined 1",
		"%global other go2spec_combine",
		"%%global go2spec_combine 1",
		"%globalgo2spec_combine 1",
		"%dnl %global go2spec_combine 1",
	}
	for i, line := range lines {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			path := aggregateUpdateFixture(t, t.TempDir(), line+"\n"+aggregateUpdateOrdinarySpec)
			spec, err := parseSpecFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := updateSpecVersion(spec, "1.1.0", "", false); err != nil {
				t.Fatalf("ordinary update rejected: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "Version: 1.1.0") || !strings.Contains(string(data), line) {
				t.Fatalf("ordinary update changed unexpectedly: %q, %v", data, err)
			}
		})
	}
}

func TestAggregateUpdateScanRejectsEvenWithoutImportPath(t *testing.T) {
	root := t.TempDir()
	aggregateUpdateFixture(t, filepath.Join(root, "go-a-ordinary"), aggregateUpdateOrdinarySpec)
	aggregateUpdateFixture(t, filepath.Join(root, "go-z-combined"), "%global go2spec_combine 1\nName: go-combined\n")
	_, err := scanSpecDir(root)
	aggregateUpdateWantUnsupported(t, err)
}

func TestAggregateUpdateDirectEntrypointsNoNetworkOrWrites(t *testing.T) {
	entrypoints := []struct {
		name string
		call func(*specInfo) error
	}{
		{"checkLatestVersion", func(s *specInfo) error {
			s.HasUpdate = true
			checkLatestVersion(context.Background(), s)
			if s.HasUpdate {
				t.Error("rejected spec still reports an update")
			}
			if s.Error == "" {
				return nil
			}
			return fmt.Errorf("%s", s.Error)
		}},
		{"verifyDownload", func(s *specInfo) error { return verifyDownload(context.Background(), s) }},
		{"remoteAssetSHA256", func(s *specInfo) error {
			_, err := remoteAssetSHA256(context.Background(), s, "1.1.0")
			return err
		}},
		{"resolvedSource0URL", func(s *specInfo) error { _, err := resolvedSource0URL(s.FilePath, "1.1.0"); return err }},
		{"updateSpecVersion/hash", func(s *specInfo) error { return updateSpecVersion(s, "1.1.0", strings.Repeat("a", 64), true) }},
		{"updateSpecVersion/no-hash", func(s *specInfo) error { return updateSpecVersion(s, "1.1.0", "", false) }},
		{"verifyAndUpdateSpec/dry", func(s *specInfo) error { return verifyAndUpdateSpec(context.Background(), s, true) }},
		{"verifyAndUpdateSpec/write", func(s *specInfo) error { return verifyAndUpdateSpec(context.Background(), s, false) }},
		{"processUpdate/dry", func(s *specInfo) error { return processUpdate(context.Background(), s, true) }},
		{"processUpdate/write", func(s *specInfo) error { return processUpdate(context.Background(), s, false) }},
	}
	for _, entry := range entrypoints {
		t.Run(entry.name, func(t *testing.T) {
			calls := aggregateUpdateTransport(t, nil)
			text := aggregateUpdateOrdinarySpec + "%define go2spec_combine 0\n"
			path := aggregateUpdateFixture(t, t.TempDir(), text)
			before := aggregateUpdateStat(t, path)
			// Construct the object directly: do not rely on prior parse/scan.
			spec := &specInfo{FilePath: path, GoImportPath: "example.com/ordinary", Version: "1.0.0", LatestVersion: "v1.1.0"}
			aggregateUpdateWantUnsupported(t, entry.call(spec))
			if got := calls.Load(); got != 0 {
				t.Errorf("made %d forbidden HTTP attempts", got)
			}
			aggregateUpdateUnchanged(t, path, text, before)
		})
	}
}

func TestAggregateUpdateCLIRejectsBeforeNetwork(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry=%t", dry), func(t *testing.T) {
			calls := aggregateUpdateTransport(t, nil)
			root := t.TempDir()
			ordinary := aggregateUpdateFixture(t, filepath.Join(root, "go-a-ordinary"), aggregateUpdateOrdinarySpec)
			text := aggregateUpdateOrdinarySpec + "%global go2spec_combine 1\n"
			combined := aggregateUpdateFixture(t, filepath.Join(root, "go-z-combined"), text)
			beforeOrdinary, beforeCombined := aggregateUpdateStat(t, ordinary), aggregateUpdateStat(t, combined)
			args := []string{"-j", "2"}
			if dry {
				args = append(args, "-n")
			}
			code, output := aggregateUpdateCLI(t, append(args, root))
			if code == 0 || !strings.Contains(output, "unsupported") || !strings.Contains(output, "go2spec_combine") {
				t.Errorf("CLI exit=%d output=%q, want nonzero unsupported", code, output)
			}
			if calls.Load() != 0 {
				t.Errorf("CLI made %d forbidden HTTP attempts", calls.Load())
			}
			aggregateUpdateUnchanged(t, ordinary, aggregateUpdateOrdinarySpec, beforeOrdinary)
			aggregateUpdateUnchanged(t, combined, text, beforeCombined)
		})
	}
}

func TestAggregateUpdateDirectCallRechecksChangedSpec(t *testing.T) {
	calls := aggregateUpdateTransport(t, nil)
	path := aggregateUpdateFixture(t, t.TempDir(), aggregateUpdateOrdinarySpec)
	spec, err := parseSpecFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := aggregateUpdateOrdinarySpec + "%global go2spec_combine 1\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	before := aggregateUpdateStat(t, path)
	spec.LatestVersion = "v1.1.0"
	aggregateUpdateWantUnsupported(t, processUpdate(context.Background(), spec, false))
	aggregateUpdateWantUnsupported(t, updateSpecVersion(spec, "1.1.0", "", false))
	if calls.Load() != 0 {
		t.Errorf("made %d forbidden HTTP attempts", calls.Load())
	}
	aggregateUpdateUnchanged(t, path, text, before)
}

func TestAggregateUpdateUnreadableFileStopsNetwork(t *testing.T) {
	calls := aggregateUpdateTransport(t, nil)
	spec := &specInfo{FilePath: filepath.Join(t.TempDir(), "missing.spec"), GoImportPath: "example.com/ordinary", LatestVersion: "v1.1.0"}
	if err := processUpdate(context.Background(), spec, false); err == nil {
		t.Error("accepted unreadable spec")
	}
	checkLatestVersion(context.Background(), spec)
	if spec.Error == "" || calls.Load() != 0 {
		t.Errorf("error=%q HTTP attempts=%d", spec.Error, calls.Load())
	}
}

func aggregateUpdateOrdinaryResponse(req *http.Request) *http.Response {
	switch {
	case strings.Contains(req.URL.Path, "/package/"):
		return aggregateUpdateResponse(req, 200, `{"modulePath":"example.com/ordinary","path":"example.com/ordinary","version":"v1.1.0","name":"ordinary"}`)
	case strings.Contains(req.URL.Path, "/module/"):
		return aggregateUpdateResponse(req, 200, `{"path":"example.com/ordinary","version":"v1.1.0","repoUrl":"https://github.com/example/ordinary"}`)
	default:
		return aggregateUpdateResponse(req, 200, "ordinary source bytes")
	}
}

func TestAggregateUpdateOrdinaryCLICompatibility(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry=%t", dry), func(t *testing.T) {
			calls := aggregateUpdateTransport(t, aggregateUpdateOrdinaryResponse)
			root := t.TempDir()
			text := "# %global go2spec_combine 1 is a documentation example only\n" + aggregateUpdateOrdinarySpec
			path := aggregateUpdateFixture(t, filepath.Join(root, "go-ordinary"), text)
			before := aggregateUpdateStat(t, path)
			args := []string{"-j", "1"}
			if dry {
				args = append(args, "-n")
			}
			code, output := aggregateUpdateCLI(t, append(args, root))
			if code != 0 || strings.Contains(output, "unsupported") || calls.Load() == 0 {
				t.Fatalf("ordinary CLI exit=%d calls=%d output=%q", code, calls.Load(), output)
			}
			if dry {
				aggregateUpdateUnchanged(t, path, text, before)
				return
			}
			data, err := os.ReadFile(path)
			wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte("ordinary source bytes")))
			if err != nil || !strings.Contains(string(data), "Version: 1.1.0") || !strings.Contains(string(data), "sha256:"+wantHash) {
				t.Fatalf("ordinary update content=%q err=%v", data, err)
			}
		})
	}
}

func TestAggregateUpdateMetadataOnlyCompatibility(t *testing.T) {
	calls := aggregateUpdateTransport(t, aggregateUpdateOrdinaryResponse)
	spec := &specInfo{GoImportPath: "example.com/ordinary", Version: "1.0.0"}
	checkLatestVersion(context.Background(), spec)
	if spec.Error != "" || !spec.HasUpdate || calls.Load() == 0 {
		t.Fatalf("metadata-only check changed: %+v HTTP calls=%d", spec, calls.Load())
	}
}
