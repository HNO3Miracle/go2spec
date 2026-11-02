package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryFixturePrimary(t *testing.T) []byte {
	t.Helper()
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">
  <package type="rpm">
    <name>go-fixture-one</name><arch>noarch</arch>
    <version epoch="0" ver="1.2.3" rel="4.or"/>
    <location href="noarch/go-fixture-one.rpm"/>
    <format><rpm:provides><rpm:entry name="go(example.test/dep/pkg)" flags="EQ" epoch="0" ver="1.2.3" rel="4.or"/></rpm:provides></format>
  </package>
  <package type="rpm">
    <name>go-fixture-two</name><arch>x86_64</arch>
    <version epoch="0" ver="2.0.0" rel="1.or"/>
    <location href="x86_64/go-fixture-two.rpm"/>
    <format><rpm:provides><rpm:entry name="go(example.test/dep/pkg)" flags="EQ" epoch="0" ver="2.0.0" rel="1.or"/><rpm:entry name="go(example.test/dep/pkg/sub)" flags="EQ" epoch="0" ver="2.0.0" rel="1.or"/></rpm:provides></format>
  </package>
  <package type="rpm">
    <name>go-fixture-riscv</name><arch>riscv64</arch>
    <version epoch="0" ver="9.0.0" rel="1.or"/>
    <location href="riscv64/go-fixture-riscv.rpm"/>
    <format><rpm:provides><rpm:entry name="go(example.test/dep/pkg)" flags="EQ" epoch="0" ver="9.0.0" rel="1.or"/></rpm:provides></format>
  </package>
  <package type="rpm">
    <name>go-fixture-unversioned</name><arch>noarch</arch>
    <version epoch="0" ver="1.0" rel="1.or"/>
    <location href="noarch/go-fixture-unversioned.rpm"/>
    <format><rpm:provides><rpm:entry name="go(example.test/unversioned)"/></rpm:provides></format>
  </package>
</metadata>`)
}

func repositoryFixtureFiles(t *testing.T) (repomd, primary []byte) {
	t.Helper()
	primaryOpen := repositoryFixturePrimary(t)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(primaryOpen); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	compressedBytes := compressed.Bytes()
	compressedHash := sha256.Sum256(compressedBytes)
	openHash := sha256.Sum256(primaryOpen)
	repomd = []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <revision>fixture-1</revision>
  <data type="primary">
    <checksum type="sha256">%s</checksum>
    <open-checksum type="sha256">%s</open-checksum>
    <location href="repodata/primary.xml.gz"/>
    <size>%d</size><open-size>%d</open-size>
  </data>
</repomd>`, hex.EncodeToString(compressedHash[:]), hex.EncodeToString(openHash[:]), len(compressedBytes), len(primaryOpen)))
	return repomd, compressedBytes
}

func repositoryFixtureServer(t *testing.T) (*httptest.Server, repositoryOptions) {
	t.Helper()
	repomd, primary := repositoryFixtureFiles(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			_, _ = w.Write(repomd)
		case "/repodata/primary.xml.gz":
			_, _ = w.Write(primary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, repositoryOptions{
		Distro:     "fixture",
		Repository: "fixture-repository",
		BaseURL:    server.URL + "/",
		Arch:       "x86_64",
		HTTPClient: server.Client(),
	}
}

func TestLoadRepositoryCatalogVerifiesSnapshotAndArchitecture(t *testing.T) {
	server, options := repositoryFixtureServer(t)
	catalog, err := loadRepositoryCatalog(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Snapshot.Revision != "fixture-1" || catalog.Snapshot.SignatureStatus != "signature-not-requested" || catalog.Snapshot.PackageCount != 4 {
		t.Fatalf("unexpected snapshot: %+v", catalog.Snapshot)
	}
	if len(catalog.Providers["go(example.test/dep/pkg)"]) != 2 {
		t.Fatalf("providers from %s = %+v", server.URL, catalog.Providers["go(example.test/dep/pkg)"])
	}
	if len(catalog.Providers["go(example.test/dep/pkg/sub)"]) != 1 {
		t.Fatalf("subpath provider missing: %+v", catalog.Providers)
	}
	if len(catalog.Providers["go(example.test/unversioned)"]) != 1 {
		t.Fatal("noarch unversioned provider missing")
	}
}

func TestLoadRepositoryCatalogRejectsChecksumMismatch(t *testing.T) {
	repomd, primary := repositoryFixtureFiles(t)
	repomd = bytes.Replace(repomd, []byte("fixture-1"), []byte("fixture-2"), 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "repomd.xml") {
			_, _ = w.Write(repomd)
			return
		}
		_, _ = w.Write(primary)
	}))
	t.Cleanup(server.Close)
	options := repositoryOptions{Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64", HTTPClient: server.Client()}
	// Alter the primary bytes while leaving both checksums in repomd unchanged.
	primary[0] ^= 1
	if _, err := loadRepositoryCatalog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum mismatch was accepted: %v", err)
	}
}

func TestRepositorySignaturePolicyRequiresKeyring(t *testing.T) {
	_, options := repositoryFixtureServer(t)
	options.RequireSignature = true
	if _, err := loadRepositoryCatalog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("missing keyring was not rejected: %v", err)
	}
}

func TestRepositoryURLAndLimits(t *testing.T) {
	for _, raw := range []string{"http://example.test/repo/", "https://user:pass@example.test/repo/", "https://example.test/repo?x=1", "https://example.test/repo/#x"} {
		if _, err := normalizeRepositoryOptions(repositoryOptions{Distro: "x", Repository: "r", BaseURL: raw, Arch: "x86_64"}); err == nil {
			t.Errorf("unsafe repository URL accepted: %s", raw)
		}
	}
	if _, err := normalizeRepositoryOptions(repositoryOptions{Distro: "x", Repository: "r", BaseURL: "https://example.test/repo/", Arch: "src"}); err == nil {
		t.Fatal("source architecture accepted")
	}
}

func TestLoadRepositoryCatalogFiltersTargetArchitecture(t *testing.T) {
	_, options := repositoryFixtureServer(t)
	options.Arch = "riscv64"
	catalog, err := loadRepositoryCatalog(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Providers["go(example.test/dep/pkg)"]) != 2 {
		t.Fatalf("riscv64/noarch providers = %+v", catalog.Providers["go(example.test/dep/pkg)"])
	}
	for _, provider := range catalog.Providers["go(example.test/dep/pkg)"] {
		if provider.Arch != "riscv64" && provider.Arch != "noarch" {
			t.Fatalf("wrong architecture provider retained: %+v", provider)
		}
	}
}

func TestLoadRepositoryCatalogRejectsUnsupportedPrimaryCompression(t *testing.T) {
	repomd, primary := repositoryFixtureFiles(t)
	repomd = bytes.Replace(repomd, []byte("primary.xml.gz"), []byte("primary.xml.xz"), 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "repomd.xml") {
			_, _ = w.Write(repomd)
			return
		}
		_, _ = w.Write(primary)
	}))
	t.Cleanup(server.Close)
	options := repositoryOptions{Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64", HTTPClient: server.Client()}
	if _, err := loadRepositoryCatalog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unsupported primary metadata compression") {
		t.Fatalf("unsupported primary compression was accepted: %v", err)
	}
}

func TestLoadRepositoryCatalogRejectsOversizedRepomd(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "repomd.xml") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxRepomdBytes+1))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	options := repositoryOptions{Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64", HTTPClient: server.Client()}
	if _, err := loadRepositoryCatalog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized repomd was accepted: %v", err)
	}
}

func TestRepositorySignatureVerifierInjection(t *testing.T) {
	repomd, primary := repositoryFixtureFiles(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			_, _ = w.Write(repomd)
		case "/repodata/repomd.xml.asc":
			_, _ = w.Write([]byte("fixture signature"))
		case "/repodata/primary.xml.gz":
			_, _ = w.Write(primary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	keyring := filepath.Join(t.TempDir(), "trusted-keyring")
	if err := os.WriteFile(keyring, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	catalog, err := loadRepositoryCatalog(context.Background(), repositoryOptions{
		Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64",
		Keyrings: []string{keyring}, RequireSignature: true, HTTPClient: server.Client(),
		VerifySignature: func(_ string, gotRepomd, gotSignature []byte, gotKeys []string) (string, error) {
			called = true
			if !bytes.Equal(gotRepomd, repomd) || string(gotSignature) != "fixture signature" || len(gotKeys) != 1 {
				t.Fatalf("unexpected signature inputs")
			}
			return "fixture-fingerprint", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || catalog.Snapshot.SignatureStatus != "verified" || catalog.Snapshot.SignatureFingerprint != "fixture-fingerprint" {
		t.Fatalf("signature verification result not recorded: %+v", catalog.Snapshot)
	}
}

func TestLoadRepositoryCatalogRejectsMissingPackageLocation(t *testing.T) {
	primaryOpen := strings.Replace(string(repositoryFixturePrimary(t)), `<location href="noarch/go-fixture-one.rpm"/>`, "", 1)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(primaryOpen)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	compressedBytes := compressed.Bytes()
	compressedHash := sha256.Sum256(compressedBytes)
	openHash := sha256.Sum256([]byte(primaryOpen))
	repomd := []byte(fmt.Sprintf(`<repomd xmlns="http://linux.duke.edu/metadata/repo"><revision>bad-location</revision><data type="primary"><checksum type="sha256">%s</checksum><open-checksum type="sha256">%s</open-checksum><location href="repodata/primary.xml.gz"/><size>%d</size><open-size>%d</open-size></data></repomd>`, hex.EncodeToString(compressedHash[:]), hex.EncodeToString(openHash[:]), len(compressedBytes), len(primaryOpen)))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "repomd.xml") {
			_, _ = w.Write(repomd)
			return
		}
		_, _ = w.Write(compressedBytes)
	}))
	t.Cleanup(server.Close)
	options := repositoryOptions{Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64", HTTPClient: server.Client()}
	if _, err := loadRepositoryCatalog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "location") {
		t.Fatalf("package without location was accepted: %v", err)
	}
}

func TestRepositoryRequirementDiagnostics(t *testing.T) {
	compare := func(_ context.Context, left, right rpmEVR) (int, error) {
		return strings.Compare(left.Version, right.Version), nil
	}
	provider := func(name, version string) rpmProvider {
		return rpmProvider{Capability: "go(example.test/dep/pkg)", Package: name, Arch: "noarch", PackageEVR: rpmEVR{Epoch: "0", Version: version, Release: "1.or"}, Flags: "EQ", ProvideEVR: &rpmEVR{Epoch: "0", Version: version, Release: "1.or"}}
	}
	cases := []struct {
		name   string
		provs  []rpmProvider
		req    *rpmEVRRequirement
		status string
	}{
		{"present", []rpmProvider{provider("one", "1.0")}, nil, "present-no-version-requirement"},
		{"satisfied", []rpmProvider{provider("one", "2.0")}, &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Version: "1.0"}}, "satisfied"},
		{"insufficient", []rpmProvider{provider("one", "1.0")}, &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Version: "2.0"}}, "version-insufficient"},
		{"multiple", []rpmProvider{provider("one", "1.0"), provider("two", "1.0")}, nil, "multiple-providers"},
		{"unknown", []rpmProvider{{Capability: "go(example.test/dep/pkg)", Package: "one", Arch: "noarch"}}, &rpmEVRRequirement{Operator: ">=", EVR: rpmEVR{Version: "1.0"}}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := diagnoseRepositoryProvider(context.Background(), tc.provs, tc.req, compare)
			if status != tc.status {
				t.Fatalf("status=%s, want %s", status, tc.status)
			}
		})
	}
}
