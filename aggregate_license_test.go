package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
)

func TestAggregateLicenseExpression(t *testing.T) {
	for _, text := range []string{"MIT", "MIT OR Apache-2.0", "(MIT OR BSD-3-Clause) AND Apache-2.0", "GPL-2.0-only WITH Classpath-exception-2.0", "LicenseRef-reviewed"} {
		if !validLicenseExpression(text) {
			t.Errorf("valid expression rejected: %q", text)
		}
	}
	for _, text := range []string{"", "TODO", "UNKNOWN", "MIT Apache-2.0", "MIT OR", "(MIT", "MIT)", "%{lua:bad}", "MIT\nSource0: bad", "MIT WITH (BSD-3-Clause)", "(MIT OR BSD-3-Clause) WITH Exception", "MIT WITH Exception WITH Other"} {
		if validLicenseExpression(text) {
			t.Errorf("unsafe/malformed expression accepted: %q", text)
		}
	}
}

func TestAggregateVersionedLicenseMetadata(t *testing.T) {
	for _, kind := range []string{"valid", "wrong-version", "wrong-path", "not-redistributable", "wrong-bytes", "missing-file", "unknown-license", "http-error"} {
		t.Run(kind, func(t *testing.T) {
			source := moduleSource{Module: module.Version{Path: "example.test/a", Version: "v1.2.3"}, Dir: t.TempDir()}
			aggregateWriteFile(t, filepath.Join(source.Dir, "LICENSE"), []byte("Root license\r\n"))
			aggregateWriteFile(t, filepath.Join(source.Dir, "child", "LICENSE-MIT"), []byte("Child license\n"))
			metadata := pkgsiteModule{Path: source.Module.Path, Version: source.Module.Version, IsRedistributable: true, Licenses: []pkgsiteLicense{
				{FilePath: "LICENSE", Types: []string{"BSD-3-Clause"}, Contents: "Root license\n"},
				{FilePath: "child/LICENSE-MIT", Types: []string{"MIT"}, Contents: "Child license\n"},
			}}
			switch kind {
			case "wrong-version":
				metadata.Version = "v1.2.4"
			case "wrong-path":
				metadata.Path = "example.test/other"
			case "not-redistributable":
				metadata.IsRedistributable = false
			case "wrong-bytes":
				metadata.Licenses[0].Contents = "Another version's license"
			case "missing-file":
				metadata.Licenses = metadata.Licenses[:1]
			case "unknown-license":
				metadata.Licenses[1].Types = []string{"UNKNOWN"}
			}
			body, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			calls := aggregateUpdateTransport(t, func(req *http.Request) *http.Response {
				if req.URL.Query().Get("version") != source.Module.Version || !strings.Contains(req.URL.Path, "/module/") {
					t.Errorf("license lookup was not pinned to the selected module/version: %s", req.URL)
				}
				if kind == "http-error" {
					return aggregateUpdateResponse(req, http.StatusNotFound, `{"message":"not indexed"}`)
				}
				return aggregateUpdateResponse(req, http.StatusOK, string(body))
			})
			license, err := moduleLicense(context.Background(), &source, "")
			if calls.Load() != 1 {
				t.Errorf("unexpected metadata request count: %d", calls.Load())
			}
			if kind == "valid" {
				if err != nil || license != "BSD-3-Clause AND MIT" || !reflect.DeepEqual(source.LicenseFiles, []string{"LICENSE", "child/LICENSE-MIT"}) {
					t.Fatalf("lost source license coverage: %q %+v %v", license, source.LicenseFiles, err)
				}
			} else if err == nil {
				t.Fatalf("accepted mismatched or incomplete license metadata: %s", license)
			}
		})
	}
}

func TestAggregateExplicitLicenseStillRequiresSourceMaterial(t *testing.T) {
	calls := aggregateUpdateTransport(t, nil)
	source := moduleSource{Module: module.Version{Path: "example.test/a", Version: "v1.0.0"}, Dir: t.TempDir()}
	if _, err := moduleLicense(context.Background(), &source, "MIT"); err == nil {
		t.Fatal("explicit expression replaced missing license material")
	}
	aggregateWriteFile(t, filepath.Join(source.Dir, "license_apache"), []byte("reviewed source license\n"))
	license, err := moduleLicense(context.Background(), &source, "Apache-2.0")
	if err != nil || license != "Apache-2.0" || len(source.LicenseFiles) != 1 || calls.Load() != 0 {
		t.Fatalf("reviewed expression did not retain source license without HTTP: %q %v requests=%d", license, err, calls.Load())
	}
}

func TestAggregateLicenseMetadataTimeout(t *testing.T) {
	oldTimeout := pkgsiteModuleTimeout
	t.Cleanup(func() { pkgsiteModuleTimeout = oldTimeout })
	pkgsiteModuleTimeout = 10 * time.Millisecond
	oldClient, oldBase := pkgsiteHTTPClient, pkgsiteAPIBase
	t.Cleanup(func() { pkgsiteHTTPClient, pkgsiteAPIBase = oldClient, oldBase })
	pkgsiteAPIBase = "https://pkgsite.invalid/v1beta"
	pkgsiteHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	source := moduleSource{Module: module.Version{Path: "example.test/timeout", Version: "v1.0.0"}, Dir: t.TempDir()}
	aggregateWriteFile(t, filepath.Join(source.Dir, "LICENSE"), []byte("license\n"))
	started := time.Now()
	_, err := moduleLicense(context.Background(), &source, "")
	if err == nil || !strings.Contains(err.Error(), "cannot obtain versioned license metadata") {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("metadata timeout took too long: %s", time.Since(started))
	}
}
func TestModuleOriginOptionalConsistency(t *testing.T) {
	for _, tc := range []struct {
		version string
		origin  *moduleOrigin
		valid   bool
	}{
		{"v1.2.3", nil, true},
		{"v1.2.3", &moduleOrigin{Ref: "refs/tags/sub/v1.2.3"}, true},
		{"v1.2.3", &moduleOrigin{Ref: "refs/tags/sub/v1.2.4"}, false},
		{"v2.0.0+incompatible", &moduleOrigin{Ref: "refs/tags/v2.0.0"}, true},
		{"v0.0.0-20200102030405-abcdef123456", &moduleOrigin{Hash: "abcdef1234567890"}, true},
		{"v0.0.0-20200102030405-abcdef123456", &moduleOrigin{Hash: "0123456789abcdef"}, false},
	} {
		if err := checkModuleOrigin(tc.version, tc.origin); (err == nil) != tc.valid {
			t.Errorf("origin check for %s: %v, valid=%t", tc.version, err, tc.valid)
		}
	}
	work, mod, result, _ := moduleSourceFixture(t)
	aggregateWriteFile(t, result.Info, []byte(`{"Version":"v1.2.3","Origin":{"Ref":"refs/tags/v1.2.4"}}`))
	if _, err := verifyModuleSource(context.Background(), work, mod, "20261019", 0, result); err == nil {
		t.Fatal("source verifier ignored contradictory Origin in .info")
	}
}
