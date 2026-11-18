# `pack` command

[![中文](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](../zh/pack.zh.md)
[![update command](https://img.shields.io/badge/docs-update-blue)](update.en.md)

`pack` generates an RPM spec draft and a source archive from a Go import path.

**This command creates a starting point for further maintenance, not a final package ready to submit. Review the generated spec before submitting it to openRuyi or building it.**

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
    Check out a Git revision, such as a tag, branch or commit

-force_prerelease
    Package HEAD or tip instead of preferring the latest tag

-type <type>
    Select library, program, library+program or program+library

-program_package_name <name>
    Override the program package name and, when appropriate, the source package name

-allow_unknown_hoster
    Allow an unknown hoster when generating the package name; review the result manually
```

## Output

A successful run creates these files in the current directory:

- `<package-name>/`, the package working directory
- `<package-name>/<package-name>.spec`, the generated RPM spec draft
- `<package-name>_<version>.orig.tar.<gz|xz>`, the upstream source archive

If the target package directory already exists and is not empty, go2spec stops to avoid overwriting existing work.

## Package types

- `library`, for Go library modules
- `program`, for command packages whose root package is `main`
- `library+program`, for projects that ship both libraries and command subpackages
- `program+library`, for program-first projects that also need a library subpackage

If `-type` is omitted, go2spec infers the type from pkg.go.dev package metadata and local `go list` output.

## Combine independently released Modules into one RPM

Use `-combine` to enable the *Independent releases* mode: **each Module uses its own selected release and corresponding source, and the selected Modules are combined into one RPM.**

Each Module uses its specified version and corresponding Go Module Proxy Source; the Modules do not need to share a repository snapshot.

```bash
# The following paths are examples; replace them with real Modules and versions.
go2spec pack \
  -combine example.org/sdk \
  -url https://example.org/sdk \
  -package_name go-example-sdk \
  -package_version 20261019 \
  example.org/sdk/a@v1.2.3 \
  example.org/sdk/b/v2@v2.0.0
```

| Flag | Meaning |
|---|---|
| `-combine <import-root>` | Common installation path and auto-Provides scan root; the root itself does not need a Go package. |
| `-url <project-homepage>` | Upstream project homepage written to `URL`; required when Modules come from multiple repositories or the homepage cannot be inferred reliably. |
| `-package_name <name>` | Explicit RPM package name; required in combine mode. |
| `-package_version <version>` | Explicit RPM `Version`, without Epoch/Release, kept separate from the Module versions. |
| `-license <expression>` | Optional license expression reviewed by the maintainer and covering **all** selected sources; actual license files are still required. |

Before choosing the `module@version` inputs, maintainers can analyze a project pinned to a specific version:

```bash
go2spec module-select --format text github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

This command analyzes dependencies to assist Module selection. See the [Module Select guide](module-select.en.md) for details. Review the usage evidence and Go-selected versions, then explicitly pass the final chosen sources to `pack -combine`.

Each argument must be a complete `module@canonical-version`. Branches, `latest`, commits without a version, duplicate Module paths (even at different versions), path-major mismatches, and Modules outside the common root are rejected.

### License, dependencies and build review

Without `-license`, go2spec retrieves metadata for each **pinned Module version** and checks the detected license-file contents (normalizing CRLF only). Missing or inconsistent contents are errors. Use `-license` only after reviewing the licenses for all selected sources. `LICENSE`, `LICENCE`, `COPYING`, `NOTICE`, and filename variants using dots, hyphens or underscores are recognized; each Module's licenses are placed in a separate subdirectory to prevent files such as `LICENSE` from overwriting one another.

Dependency analysis scans imports separately for Linux `amd64` and `riscv64`, using the local Go command's release/tool build tags and baseline CPU features. Custom `GOFLAGS` and `GOEXPERIMENT` are cleared. For a noarch combined RPM, the library-package, BuildRequires and Requires sets must match across both architectures; any difference is an error. Dependencies used only by tests, commands or examples go into BuildRequires; if a shipped library also needs them, they go into Requires.
