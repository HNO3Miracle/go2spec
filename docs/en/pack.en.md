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
