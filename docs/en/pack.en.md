# pack Command

[![中文](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](../zh/pack.zh.md)
[![update command](https://img.shields.io/badge/docs-update-blue)](update.en.md)

`pack` generates an RPM spec draft and a source archive from a Go import path.

**This command is intended to produce a maintainable starting point, not a final package that can be submitted directly. Always review the generated spec manually before submitting it to openRuyi or using it for builds.**

## Usage

```bash
go2spec pack [FLAG]... <go-package-importpath>
```

Example:

```bash
go2spec pack golang.org/x/net
```

## Flags

```text
-git_revision <rev>
    Check out a specific git revision, such as a tag, branch, or commit

-force_prerelease
    Package HEAD or tip instead of preferring the latest tagged version

-type <type>
    Select the package type, supported values are library, program, library+program, and program+library

-program_package_name <name>
    Override the program package name, and the source package name when appropriate

-allow_unknown_hoster
    Allow package-name generation for unknown hosters, Review the generated name manually
```

## Output

A successful run creates these files in the current directory:

- `<package-name>/`, package working directory
- `<package-name>/<package-name>.spec`, generated RPM spec draft
- `<package-name>_<version>.orig.tar.<gz|xz>`, upstream source archive

If the target package directory already exists and is not empty, go2spec aborts to avoid overwriting existing work.

## Package Types

- `library`, for Go library modules
- `program`, for command packages whose root package is `main`
- `library+program`, for projects that ship both library files and command subpackages
- `program+library`, for program-first projects that also need a library subpackage

When `-type` is omitted, go2spec uses pkg.go.dev package metadata and local `go list` output to infer the type.

## Combine Independent Module Releases into One RPM

`-combine` opts into a separate **library source RPM spec** mode. Each module keeps
its explicitly selected release and uses a fixed Go Module Proxy Source URL. go2spec
may download temporary archives locally for validation, but successful output contains
the spec only; OBS source services download the archives from `SourceN` and verify
`#!RemoteAsset` during source preparation. Without this flag, multiple positional
arguments still mean separate packages; the existing single-module, list and program
modes are unchanged.

```bash
# Illustrative module paths; replace them with real modules and versions.
go2spec pack \
  -combine example.org/sdk \
  -url https://example.org/sdk \
  -package_name go-example-sdk \
  -package_version 20261019 \
  example.org/sdk/a@v1.2.3 \
  example.org/sdk/b/v2@v2.0.0
```

Put flags before positional arguments, as with other Go flag-based commands.

| Flag | Meaning |
|---|---|
| `-combine <import-root>` | Common installation and auto-Provides scan root. This need not itself contain a Go package. |
| `-url <project-homepage>` | Upstream project homepage written to `URL`; required when a common homepage cannot be determined. |
| `-package_name <name>` | Explicit RPM name; required with `-combine`. |
| `-package_version <version>` | Explicit RPM `Version`, without Epoch/Release; independent of module versions. |
| `-license <expression>` | Optional, manually reviewed license expression covering **all** selected sources. Actual license files are still required. |

Every input must be a complete `module@canonical-version`. Branches, `latest`,
unversioned commits, duplicate module paths (even at different versions),
path-major mismatches and modules outside the common root are rejected. The
slash boundary matters: `example.org/sdk-extra` is not under `example.org/sdk`.
Do not combine this mode with `-git_revision`, `-force_prerelease`, `-list`,
`-program_package_name`, `-allow_unknown_hoster`, or non-library `-type` values.

### Sources, Versions and Output

For the example above, the output contains only the spec:

```text
go-example-sdk/
  go-example-sdk.spec
```

A generated Source entry looks like:

```spec
URL:            https://example.org/sdk
#!RemoteAsset:  sha256:<module-a-zip-sha256>
Source0:        https://proxy.golang.org/example.org/sdk/a/@v/v1.2.3.zip#/module-0-v1.2.3.zip
```

Temporary ZIPs may exist locally while go2spec validates them, but they are not repository inputs.

Source numbering follows sorted module paths, not argument order. Each Source is a fixed Go
Module Proxy URL immediately preceded by its `#!RemoteAsset: sha256:...` directive. OBS source
services download and verify these URLs during source preparation; the RPM build remains offline.
No developer cache path, credential-bearing URL, local ZIP, or extra lock manifest is written, and
maintainers do not need to add a hand-written `_service`.

Resolution uses `go mod download -json module@version` in a private module/cache,
with `GOWORK=off` and `GOTOOLCHAIN=local`. User Go network/checksum settings are
respected. Returned identity, `.info`, the module directive, and the ZIP's own
`go.mod` are checked; contradictory optional Origin tags/pseudo-version hashes
are rejected. SHA256 pins bytes, not independently authenticated release identity.
The generated public Source URL is fetched and its exact ZIP bytes are compared with the local
validation download before publication. A proxy that serves a different ZIP, or a direct-only
module that is not available from `proxy.golang.org`, fails rather than emitting a checksum that
OBS would later reject.

The spec records canonical Go versions separately from RPM-compatible module
versions in a complete two-column `go_submodules` table. Releases lose the leading
`v`; prereleases use `~`. Pseudo-versions use the **generation/packaging date**, not
the upstream commit timestamp. `+incompatible` is retained in Go identity but not
RPM version text. This conversion is not a lossless, universally order-preserving
replacement for Go versions. Hyphenated prerelease labels and unsupported snapshot
revision formats currently fail instead of being silently rewritten.

All selected sources must pass validation before publishing output. Existing
output paths, including empty directories, are refused. On ordinary errors,
private temporary sources are cleaned and no successful spec is published.
On Linux with kernel/filesystem support for `RENAME_NOREPLACE`, the complete
spec directory is published in one atomic rename; otherwise publication fails
without replacing an existing path. If the process stops before publication,
the next invocation removes only inactive matching staging trees carrying
go2spec's ownership marker and retries. Active staging trees, unmarked paths,
and unrelated user paths are left untouched. This guarantees namespace
publication, not filesystem crash durability without an explicit sync. Combined
publication is rejected on non-Linux systems. Never treat an interrupted output
directory without its final spec as complete.

### Licenses, Dependencies and Build Review

Without `-license`, metadata is requested for each **exact module version**, and
all recognized source license files must match it (apart from CRLF normalization).
Missing/inconsistent metadata is an error, not a `TODO` license. Use `-license`
only after reviewing all source licenses; syntax checks are not legal review.
Modules matching any of Go's private-module protection settings—`GOPRIVATE`, `GONOPROXY`, or
`GONOSUMDB`—cannot use this mode's public Go Module Proxy `SourceN` URLs. The tool
requires a future private Source workflow and does not send such paths to public license
metadata. Supplying `-license` does not override this Source safety restriction.
`LICENSE`, `LICENCE`, `COPYING`, `NOTICE` and their dot/hyphen/underscore variants
are retained under separate per-module license directories.

Dependency discovery separately analyzes Linux `amd64` and `riscv64` imports using the local
Go command's release/tool tags and baseline CPU features. Custom `GOFLAGS` and `GOEXPERIMENT`
are cleared. For a noarch combined package, the library-package, BuildRequires and Requires
sets must agree across both architectures; an architecture-only provider is not emitted as an
unconditional dependency. Standard packages and actual internal libraries are not emitted as
external dependencies. External imports retain their **full package path** in `go(path)`
capabilities; test/command/example-only dependencies are build-only unless also needed by a
delivered library. A required but unselected nested module is external, not silently bundled.
This is not MVS, provider lookup, or proof that the target repository contains API-compatible
dependencies. Review the result against the target Go version/build tags and rebuild when they change.

The spec installs all selected source trees first, then calls
`%go_generate_provides` once. It does not write ordinary `Provides` by hand or
invent a root capability when the root has no package. A custom `%install`
replaces the default single-tree installation. `%check` runs Go tests in GOPATH
mode with module downloads disabled, without skipping failed tests. This does
not sandbox network calls made by the upstream tests themselves.

### Initial Support Boundary

- Requires actual `go.mod` files in module ZIPs and a sufficient local Go toolchain;
  no automatic toolchain download or legacy synthetic `go.mod` fallback.
- Rejects `replace`/`exclude`, unmapped nested `go.mod`, conflicting parent/child
  source ownership, supplied `.goipath` markers and active Cgo files whose native
  dependencies cannot be inferred. These cases require manual packaging.
- Accepts a conservative regular-file ZIP subset. Links, special modes, unsafe
  paths, conflicting local/central headers and alternate path/type extra fields
  are rejected rather than interpreted differently by Go and system `unzip`.
- Requires `go-rpm-macros >= 0.3`, `go`, `coreutils` and `unzip` in the target build.
  Real openRuyi builds must still verify tests, installed markers, owning RPMs and
  header capabilities. A successful generation or shell-layout test is not that
  acceptance test.
- No automatic module selection, target-repository provider resolution, full
  module-graph compatibility solver or combined-spec update is implemented.
  The [legacy updater refuses marked combined specs](update.en.md#aggregate-specs-are-unsupported),
  including dry runs; do not remove `%global go2spec_combine 1` to bypass it.

## Native RPM Acceptance Test

The ordinary Go suite includes `TestAggregateNativeFixture`. It generates two
independent module ZIPs through a local file proxy, invokes the pack CLI entry
point, and runs the fixture's own Go tests with downloads disabled. It does
**not** skip when RPM is unavailable and does **not** claim to build an RPM.

To export the same fixture for a real openRuyi build, run from the go2spec source
directory. Choose an absolute output path that does not exist:

```bash
GO2SPEC_NATIVE_FIXTURE_DIR=/tmp/go2spec-native-fixture \
  go test -count=1 -run '^TestAggregateNativeFixture$' .
```

The fixture contains a parent module at `v1.2.3` and a nested `sub` module at
`v0.4.0`. The aggregate RPM version is deliberately `99.0`, so accidental fallback
to `%{version}` is detectable. The expected library capabilities are:

| Import path suffix under `example.test/go2spec/fixture` | RPM version |
|---|---|
| `/base` | `1.2.3` |
| `/sub` | `0.4.0` |
| `/sub/leaf` | `0.4.0` |
| `/submarine` | `1.2.3` — not a child of `/sub` |

The root has no package. Command, example and test-only directories must not
acquire capabilities. The parent library imports the nested module, and both
modules have tests. `expected-provides.txt` is an assertion list, not a fabricated
`.goipath` or a stand-in for an RPM header.

Transfer the complete exported directory and `tests/native-combine.sh` into a
clean openRuyi build environment with the standard RPM build configuration and
the generated spec's BuildRequires already installed. Run as the build user:

```bash
bash tests/native-combine.sh /tmp/go2spec-native-fixture /tmp/go2spec-native-build
```

The build directory must also be new. The script installs nothing and retains
all logs and artifacts on failure. Before touching rpmdb, it checks the operating
system; it then verifies the installed `go-rpm-macros` identity, version and files.
`0.3~rc2` is lower than `0.3` and is refused, not relabeled or accepted via
`--nodeps`. No source `--load` macros, alternate rpmdb, disabled tests, or rewritten
spec versions/dependencies are used.

A pass requires a real `rpmbuild -ba`, exactly the expected owning noarch RPM,
exact Go capabilities and matching payload marker bytes, a complete source tree
matching both ZIPs byte for byte (with no extra source files), two separate intact
license copies, all four fixture tests in the build log, and unchanged input
hashes. Exit `2` means an environment prerequisite blocked the attempt; it is not
a passed or skipped native acceptance test. Unit tests, shell syntax checks and
the refusal paths alone do not verify the native success path.
