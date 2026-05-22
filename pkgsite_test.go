package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
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
