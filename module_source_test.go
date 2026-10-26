package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func moduleSourceFixture(t *testing.T) (string, module.Version, moduleDownload, map[string]string) {
	t.Helper()
	work := t.TempDir()
	mod := module.Version{Path: "example.test/sdk/a", Version: "v1.2.3"}
	files := map[string]string{
		"go.mod": "module " + mod.Path + "\n\ngo 1.18\n",
		"a.go":   "package a\n", "LICENSE": "Fixture license\n",
	}
	result := moduleDownload{
		Path: mod.Path, Version: mod.Version,
		Info:  filepath.Join(work, "modcache", "version.info"),
		GoMod: filepath.Join(work, "modcache", "version.mod"),
		Zip:   filepath.Join(work, "modcache", "version.zip"),
	}
	aggregateWriteFile(t, result.Info, []byte(`{"Version":"v1.2.3"}`))
	aggregateWriteFile(t, result.GoMod, []byte(files["go.mod"]))
	aggregateWriteFile(t, result.Zip, aggregateZIP(t, mod, files))
	return work, mod, result, files
}

func TestVerifyModuleSource(t *testing.T) {
	work, mod, result, files := moduleSourceFixture(t)
	files["go.mod"] += "toolchain go1.24.1\nrequire example.test/other v1.0.0\n"
	aggregateWriteFile(t, result.GoMod, []byte(files["go.mod"]))
	aggregateWriteFile(t, result.Zip, aggregateZIP(t, mod, files))
	source, err := verifyModuleSource(work, mod, "20261019", 7, result)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(result.Zip)
	if err != nil {
		t.Fatal(err)
	}
	if source.Module != mod || source.RPMVersion != "1.2.3" || source.GoVersion != "1.24.1" || source.SHA256 != hash || source.FileName != "module-7-v1.2.3.zip" || len(source.Requires) != 1 {
		t.Fatalf("wrong source facts: %+v", source)
	}
	data, err := os.ReadFile(filepath.Join(source.Dir, "go.mod"))
	if err != nil || string(data) != files["go.mod"] {
		t.Fatalf("wrong extracted go.mod: %q, %v", data, err)
	}
}

func TestVerifyModuleSourceRejectsInconsistentArtifacts(t *testing.T) {
	for _, kind := range []string{"info-version", "module-path", "zip-version", "zip-go-mod", "missing-go-mod", "replace", "exclude", "traversal", "marker", "bad-zip", "truncated-zip"} {
		t.Run(kind, func(t *testing.T) {
			work, mod, result, files := moduleSourceFixture(t)
			switch kind {
			case "info-version":
				aggregateWriteFile(t, result.Info, []byte(`{"Version":"v1.2.4"}`))
			case "module-path":
				files["go.mod"] = "module example.test/other\n"
				aggregateWriteFile(t, result.GoMod, []byte(files["go.mod"]))
			case "zip-version":
				mod.Version = "v1.2.2"
			case "zip-go-mod":
				files["go.mod"] += "// ZIP-only change\n"
			case "missing-go-mod":
				delete(files, "go.mod")
			case "replace", "exclude":
				if kind == "replace" {
					files["go.mod"] += "replace example.test/other => ../other\n"
				} else {
					files["go.mod"] += "exclude example.test/other v1.0.0\n"
				}
				aggregateWriteFile(t, result.GoMod, []byte(files["go.mod"]))
			case "traversal":
				files["../escaped.go"] = "package escaped\n"
			case "marker":
				files[".goipath"] = "version:99\n"
			}
			data := aggregateZIP(t, mod, files)
			if kind == "bad-zip" {
				data = []byte("not a ZIP")
			} else if kind == "truncated-zip" {
				data = data[:len(data)/2]
			}
			aggregateWriteFile(t, result.Zip, data)
			requested := module.Version{Path: result.Path, Version: result.Version}
			if _, err := verifyModuleSource(work, requested, "20261019", 0, result); err == nil {
				t.Fatal("accepted inconsistent or unsafe source")
			}
		})
	}
}

func TestModuleCacheFile(t *testing.T) {
	work, _, result, _ := moduleSourceFixture(t)
	if err := moduleCacheFile(work, result.Info, 1024); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	aggregateWriteFile(t, outside, []byte("outside"))
	link := filepath.Join(work, "modcache", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "relative", outside, link, filepath.Join(work, "modcache")} {
		if err := moduleCacheFile(work, name, 1024); err == nil {
			t.Errorf("accepted cache path %q", name)
		}
	}
	if err := moduleCacheFile(work, result.Info, 1); err == nil {
		t.Error("size bound ignored")
	}
	alias := filepath.Join(t.TempDir(), "work-link")
	if err := os.Symlink(work, alias); err != nil {
		t.Fatal(err)
	}
	if err := moduleCacheFile(alias, filepath.Join(alias, "modcache", "version.info"), 1024); err != nil {
		t.Fatalf("safe symlinked output parent rejected: %v", err)
	}
}

func TestModuleZIPExtractionChecks(t *testing.T) {
	for _, kind := range []string{"regular", "timestamp", "symlink", "fifo", "setuid", "unreadable", "duplicate", "alternate-path", "malformed-extra", "local-name", "local-flags", "local-extra", "crc"} {
		t.Run(kind, func(t *testing.T) {
			var buf bytes.Buffer
			writer := zip.NewWriter(&buf)
			header := &zip.FileHeader{Name: "example.test/a@v1.0.0/file", Method: zip.Store}
			header.SetMode(0644)
			switch kind {
			case "timestamp", "local-extra":
				header.Extra = []byte{0x55, 0x54, 5, 0, 1, 1, 0, 0, 0}
			case "symlink":
				header.SetMode(os.ModeSymlink | 0777)
			case "fifo":
				header.SetMode(os.ModeNamedPipe | 0644)
			case "setuid":
				header.SetMode(os.ModeSetuid | 0755)
			case "unreadable":
				header.SetMode(0)
			case "alternate-path":
				header.Extra = []byte{0x75, 0x70, 1, 0, 1}
			case "malformed-extra":
				header.Extra = []byte{1, 2, 3}
			}
			w, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("payload")); err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate" {
				if _, err := writer.Create(header.Name); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			switch kind {
			case "local-name":
				data[30] = 'X'
			case "local-flags":
				data[6] ^= 1
			case "local-extra":
				data[30+len(header.Name)+5] ^= 1
			case "crc":
				data[bytes.Index(data, []byte("payload"))] ^= 1
			}
			name := filepath.Join(t.TempDir(), "test.zip")
			aggregateWriteFile(t, name, data)
			err = checkModuleZipExtraction(name)
			if kind == "regular" || kind == "timestamp" || kind == "crc" {
				if err != nil {
					t.Fatal(err)
				}
				if kind == "crc" {
					// Header checks are separate from CheckZip/Unzip. The full
					// source verifier must reject corrupt payload bytes too.
					work, mod, result, files := moduleSourceFixture(t)
					valid := aggregateZIP(t, mod, files)
					r, err := zip.NewReader(bytes.NewReader(valid), int64(len(valid)))
					if err != nil {
						t.Fatal(err)
					}
					offset, err := r.File[0].DataOffset()
					if err != nil {
						t.Fatal(err)
					}
					valid[offset] ^= 0xff
					aggregateWriteFile(t, result.Zip, valid)
					if _, err := verifyModuleSource(work, mod, "20261019", 0, result); err == nil {
						t.Fatal("corrupt payload accepted")
					}
				}
			} else if err == nil {
				t.Fatal("unsafe extraction metadata accepted")
			}
		})
	}
}

func TestModuleZIPRejectsLocalExtraOnly(t *testing.T) {
	_, mod, result, files := moduleSourceFixture(t)
	data := aggregateZIP(t, mod, files)
	// Put an extra field into only the first local header. Adjust central
	// offsets so archive/zip can still open the archive normally.
	nameLen := int(binary.LittleEndian.Uint16(data[26:]))
	insert := 30 + nameLen
	extra := []byte{0x75, 0x70, 1, 0, 1}
	data = append(append(append([]byte{}, data[:insert]...), extra...), data[insert:]...)
	binary.LittleEndian.PutUint16(data[28:], uint16(len(extra)))
	eocd := bytes.LastIndex(data, []byte{'P', 'K', 5, 6})
	central := int(binary.LittleEndian.Uint32(data[eocd+16:])) + len(extra)
	binary.LittleEndian.PutUint32(data[eocd+16:], uint32(central))
	for pos := central; pos < eocd && bytes.Equal(data[pos:pos+4], []byte{'P', 'K', 1, 2}); {
		offset := binary.LittleEndian.Uint32(data[pos+42:])
		if offset != 0 {
			binary.LittleEndian.PutUint32(data[pos+42:], offset+uint32(len(extra)))
		}
		pos += 46 + int(binary.LittleEndian.Uint16(data[pos+28:])) + int(binary.LittleEndian.Uint16(data[pos+30:])) + int(binary.LittleEndian.Uint16(data[pos+32:]))
	}
	aggregateWriteFile(t, result.Zip, data)
	r, err := zip.OpenReader(result.Zip)
	if err != nil {
		t.Fatalf("fixture is not structurally readable: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkModuleZipExtraction(result.Zip); err == nil {
		t.Fatal("uninspected local-only metadata accepted")
	}
}

func TestDownloadModuleMetadataAndEnvironment(t *testing.T) {
	work := t.TempDir()
	bin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "arguments")
	aggregateWriteFile(t, filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" \"$GOWORK\" \"$GO111MODULE\" \"$GOTOOLCHAIN\" \"$GOFLAGS\" \"$GOEXPERIMENT\" \"$GOMODCACHE\" > \"$GO2SPEC_CAPTURE\"\nprintf '%s' \"$GO2SPEC_RESULT\"\nprintf '%s' 'https://user:secret@proxy.invalid' >&2\nexit \"$GO2SPEC_EXIT\"\n"))
	if err := os.Chmod(filepath.Join(bin, "go"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO2SPEC_CAPTURE", capture)
	t.Setenv("GOWORK", "/unrelated/workspace")
	t.Setenv("GOFLAGS", "-tags=unrelated")
	t.Setenv("GOEXPERIMENT", "unrelated")
	mod := module.Version{Path: "example.test/sdk/a", Version: "v1.2.3"}
	for _, output := range []string{"not JSON", "{}{}", `{"Path":"other","Version":"v1.2.3"}`, `{"Error":"https://user:secret@proxy.invalid"}`} {
		t.Setenv("GO2SPEC_RESULT", output)
		t.Setenv("GO2SPEC_EXIT", "0")
		_, err := downloadModuleSource(context.Background(), work, mod, "20261019", 0)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("metadata failure was accepted or leaked diagnostics: %v", err)
		}
	}
	t.Setenv("GO2SPEC_EXIT", "1")
	if _, err := downloadModuleSource(context.Background(), work, mod, "20261019", 0); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("command failure was accepted or leaked diagnostics: %v", err)
	}
	data, err := os.ReadFile(capture)
	want := "mod\ndownload\n-json\nexample.test/sdk/a@v1.2.3\noff\non\nlocal\n\n\n" + filepath.Join(work, "modcache") + "\n"
	if err != nil || string(data) != want {
		t.Fatalf("unexpected command/environment: %q, %v", data, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := downloadModuleSource(ctx, work, mod, "20261019", 0); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation lost: %v", err)
	}
}
