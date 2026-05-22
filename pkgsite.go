package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const pkgsiteAPIBase = "https://pkg.go.dev/v1beta"
const pkgsiteMaxResponseBytes = 20 << 20

var (
	pkgsiteHTTPClient = http.DefaultClient
	pkgsiteMu         sync.Mutex
	pkgsiteInfoCache  = make(map[string]*pkgsiteInfo)
)

type pkgsiteInfo struct {
	Package pkgsitePackage
	Module  pkgsiteModule
}

type pkgsitePackage struct {
	ModulePath          string           `json:"modulePath"`
	Version             string           `json:"version"`
	Path                string           `json:"path"`
	Name                string           `json:"name"`
	Synopsis            string           `json:"synopsis"`
	Imports             []string         `json:"imports"`
	Licenses            []pkgsiteLicense `json:"licenses"`
	IsRedistributable   bool             `json:"isRedistributable"`
	IsStandardLibrary   bool             `json:"isStandardLibrary"`
	AmbiguousCandidates []pkgsiteCandidate
}

type pkgsiteModule struct {
	Path              string           `json:"path"`
	Version           string           `json:"version"`
	RepoURL           string           `json:"repoUrl"`
	Readme            *pkgsiteReadme   `json:"readme"`
	Licenses          []pkgsiteLicense `json:"licenses"`
	IsRedistributable bool             `json:"isRedistributable"`
	IsStandardLibrary bool             `json:"isStandardLibrary"`
	HasGoMod          bool             `json:"hasGoMod"`
}

type pkgsiteReadme struct {
	Contents string `json:"contents"`
	Filepath string `json:"filepath"`
}

type pkgsiteLicense struct {
	Types    []string `json:"types"`
	FilePath string   `json:"filePath"`
	Contents string   `json:"contents"`
}

type pkgsiteCandidate struct {
	ModulePath  string `json:"modulePath"`
	PackagePath string `json:"packagePath"`
}

type pkgsiteAPIError struct {
	Code       int                `json:"code"`
	Message    string             `json:"message"`
	Fixes      []string           `json:"fixes"`
	Candidates []pkgsiteCandidate `json:"candidates"`
	Status     string             `json:"-"`
}

func (e *pkgsiteAPIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Status != "" {
		return e.Status
	}
	if e.Code != 0 {
		return fmt.Sprintf("pkgsite API error: HTTP %d", e.Code)
	}
	return "pkgsite API error"
}

func pkgsiteEscapedPath(importPath string) string {
	parts := strings.Split(strings.Trim(importPath, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func pkgsiteURL(endpoint, importPath string, values url.Values) string {
	u := pkgsiteAPIBase + "/" + endpoint + "/" + pkgsiteEscapedPath(importPath)
	if len(values) > 0 {
		u += "?" + values.Encode()
	}
	return u
}

// pkgsiteGetJSON performs a pkg.go.dev v1beta GET with up to 3 attempts and
// linear backoff for transport errors, HTTP 429, and 5xx responses.
func pkgsiteGetJSON(ctx context.Context, endpoint, importPath string, values url.Values, v any) error {
	client := pkgsiteHTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkgsiteURL(endpoint, importPath, values), nil)
		if err != nil {
			return fmt.Errorf("create pkgsite request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("pkgsite request: %w", err)
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return lastErr
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, pkgsiteMaxResponseBytes+1))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read pkgsite response: %w", err)
		}
		if len(body) > pkgsiteMaxResponseBytes {
			return fmt.Errorf("pkgsite response exceeds %d bytes", pkgsiteMaxResponseBytes)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			var apiErr pkgsiteAPIError
			if err := json.Unmarshal(body, &apiErr); err != nil || apiErr.Error() == "pkgsite API error" {
				apiErr.Message = strings.TrimSpace(string(body))
			}
			apiErr.Code = resp.StatusCode
			apiErr.Status = resp.Status
			lastErr = &apiErr
			if attempt < 3 && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return lastErr
		}

		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("decode pkgsite response: %w", err)
		}
		return nil
	}
	return lastErr
}

func getPkgsitePackage(ctx context.Context, gopkg, modulePath string) (pkgsitePackage, error) {
	values := url.Values{}
	values.Set("imports", "true")
	values.Set("licenses", "true")
	if modulePath != "" {
		values.Set("module", modulePath)
	}

	var p pkgsitePackage
	err := pkgsiteGetJSON(ctx, "package", gopkg, values, &p)
	if err == nil {
		return p, nil
	}

	var apiErr *pkgsiteAPIError
	if modulePath != "" || !isPkgsiteAPIError(err, &apiErr) || len(apiErr.Candidates) == 0 {
		return pkgsitePackage{}, err
	}

	// Ambiguous import paths return candidate module/package pairs. Retry once
	// with the longest module path, which is the most specific match.
	best := apiErr.Candidates[0]
	for _, candidate := range apiErr.Candidates[1:] {
		if len(candidate.ModulePath) > len(best.ModulePath) {
			best = candidate
		}
	}
	if best.ModulePath == "" {
		return pkgsitePackage{}, err
	}
	return getPkgsitePackage(ctx, gopkg, best.ModulePath)
}

func isPkgsiteAPIError(err error, target **pkgsiteAPIError) bool {
	return errors.As(err, target)
}

func getPkgsiteModule(ctx context.Context, modulePath, version string) (pkgsiteModule, error) {
	values := url.Values{}
	values.Set("licenses", "true")
	values.Set("readme", "true")
	if version != "" {
		values.Set("version", version)
	}

	var m pkgsiteModule
	if err := pkgsiteGetJSON(ctx, "module", modulePath, values, &m); err != nil {
		return pkgsiteModule{}, err
	}
	return m, nil
}

func getPkgsiteInfo(ctx context.Context, gopkg string) (*pkgsiteInfo, error) {
	pkgsiteMu.Lock()
	if info := pkgsiteInfoCache[gopkg]; info != nil {
		pkgsiteMu.Unlock()
		return info, nil
	}
	pkgsiteMu.Unlock()

	p, err := getPkgsitePackage(ctx, gopkg, "")
	if err != nil {
		return nil, fmt.Errorf("get pkgsite package: %w", err)
	}
	if p.ModulePath == "" {
		return nil, fmt.Errorf("pkgsite package %q has empty module path", gopkg)
	}

	m, err := getPkgsiteModule(ctx, p.ModulePath, p.Version)
	if err != nil {
		return nil, fmt.Errorf("get pkgsite module: %w", err)
	}

	info := &pkgsiteInfo{Package: p, Module: m}

	pkgsiteMu.Lock()
	pkgsiteInfoCache[gopkg] = info
	if p.Path == p.ModulePath {
		// Only cache module-path lookups when the package data also describes
		// the module root. Subpackage metadata would give callers the wrong
		// package name for later module-root lookups.
		pkgsiteInfoCache[p.ModulePath] = info
	}
	pkgsiteMu.Unlock()

	return info, nil
}

func pkgsiteLicenseExpression(licenses []pkgsiteLicense) string {
	topLevel := make([]pkgsiteLicense, 0, len(licenses))
	for _, license := range licenses {
		if !strings.Contains(strings.Trim(license.FilePath, "/"), "/") {
			topLevel = append(topLevel, license)
		}
	}
	if len(topLevel) > 0 {
		// Prefer root license files; subdirectory licenses often describe vendored
		// or generated code that should not affect the spec License field.
		licenses = topLevel
	}

	seenGroups := make(map[string]bool)
	for _, license := range licenses {
		seenTypes := make(map[string]bool)
		for _, typ := range license.Types {
			typ = strings.TrimSpace(typ)
			if typ != "" {
				seenTypes[typ] = true
			}
		}
		if len(seenTypes) == 0 {
			continue
		}
		types := make([]string, 0, len(seenTypes))
		for typ := range seenTypes {
			types = append(types, typ)
		}
		sort.Strings(types)
		group := types[0]
		if len(types) > 1 {
			group = "(" + strings.Join(types, " OR ") + ")"
		}
		seenGroups[group] = true
	}
	if len(seenGroups) == 0 {
		return "TODO"
	}

	groups := make([]string, 0, len(seenGroups))
	for group := range seenGroups {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return strings.Join(groups, " AND ")
}
