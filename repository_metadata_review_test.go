package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepositoryPathValidatorsRejectTraversal(t *testing.T) {
	for _, href := range []string{"..", "%2e%2e", "%2E%2E", "../repodata/primary.xml.gz", "%2e%2e/repodata/primary.xml.gz", "%5c..%5csecret"} {
		t.Run(href, func(t *testing.T) {
			if _, err := resolveMetadataURL("https://repo.example.test/root/", href); err == nil {
				t.Errorf("metadata location %q was accepted", href)
			}
			if safeRepositoryLocation(href) {
				t.Errorf("package location %q was accepted", href)
			}
		})
	}
	if _, err := resolveMetadataURL("https://repo.example.test/root/", "repodata/primary.xml.gz"); err != nil {
		t.Fatalf("safe metadata path rejected: %v", err)
	}
	if !safeRepositoryLocation("noarch/package.rpm") {
		t.Fatal("safe package path rejected")
	}
}

func TestLoadRepositoryCatalogAllowsMissingOpenMetadataFields(t *testing.T) {
	repomd, primary := repositoryFixtureFiles(t)
	primaryOpen := repositoryFixturePrimary(t)
	openHash := sha256.Sum256(primaryOpen)
	checksumTag := []byte(fmt.Sprintf(`<open-checksum type="sha256">%s</open-checksum>`, hex.EncodeToString(openHash[:])))
	sizeTag := []byte(fmt.Sprintf(`<open-size>%d</open-size>`, len(primaryOpen)))
	cases := []struct {
		name           string
		omitChecksum   bool
		omitSize       bool
		checksumStatus string
		openSize       int64
	}{
		{name: "neither", omitChecksum: true, omitSize: true, checksumStatus: "not-provided"},
		{name: "checksum-only", omitSize: true, checksumStatus: "verified"},
		{name: "size-only", omitChecksum: true, checksumStatus: "not-provided", openSize: int64(len(primaryOpen))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repomdCopy := append([]byte(nil), repomd...)
			if tc.omitChecksum {
				repomdCopy = bytes.Replace(repomdCopy, checksumTag, nil, 1)
			}
			if tc.omitSize {
				repomdCopy = bytes.Replace(repomdCopy, sizeTag, nil, 1)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repodata/repomd.xml":
					_, _ = w.Write(repomdCopy)
				case "/repodata/primary.xml.gz":
					_, _ = w.Write(primary)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			options := repositoryOptions{Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64", HTTPClient: server.Client()}
			catalog, err := loadRepositoryCatalog(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.Snapshot.PrimaryOpenChecksumStatus != tc.checksumStatus || catalog.Snapshot.PrimaryOpenSize != tc.openSize {
				t.Fatalf("open metadata snapshot = status %q, size %d; want %q, %d", catalog.Snapshot.PrimaryOpenChecksumStatus, catalog.Snapshot.PrimaryOpenSize, tc.checksumStatus, tc.openSize)
			}
		})
	}
}

func TestLoadRepositoryCatalogRejectsProvidedOpenMetadataMismatch(t *testing.T) {
	repomd, primary := repositoryFixtureFiles(t)
	openHash := sha256.Sum256(repositoryFixturePrimary(t))
	checksum := hex.EncodeToString(openHash[:])
	badChecksum := strings.Repeat("0", len(checksum))
	if badChecksum == checksum {
		badChecksum = strings.Repeat("f", len(checksum))
	}
	badRepomd := bytes.Replace(repomd, []byte(checksum), []byte(badChecksum), 1)
	if bytes.Equal(badRepomd, repomd) {
		t.Fatal("fixture open checksum was not found")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			_, _ = w.Write(badRepomd)
		case "/repodata/primary.xml.gz":
			_, _ = w.Write(primary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	options := repositoryOptions{Distro: "fixture", Repository: "repo", BaseURL: server.URL + "/", Arch: "x86_64", HTTPClient: server.Client()}
	if _, err := loadRepositoryCatalog(t.Context(), options); err == nil || !strings.Contains(err.Error(), "open checksum") {
		t.Fatalf("incorrect provided open checksum was accepted: %v", err)
	}
}
