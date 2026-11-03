package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadRepositoryCatalogRejectsProvidedOpenSizeMismatch(t *testing.T) {
	repomd, primary := repositoryFixtureFiles(t)
	openSize := len(repositoryFixturePrimary(t))
	goodSize := []byte(fmt.Sprintf("<open-size>%d</open-size>", openSize))
	badSize := []byte(fmt.Sprintf("<open-size>%d</open-size>", openSize+1))
	badRepomd := bytes.Replace(repomd, goodSize, badSize, 1)
	if bytes.Equal(badRepomd, repomd) {
		t.Fatal("fixture open size was not found")
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
	if _, err := loadRepositoryCatalog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "open size") {
		t.Fatalf("incorrect provided open size was accepted: %v", err)
	}
}
