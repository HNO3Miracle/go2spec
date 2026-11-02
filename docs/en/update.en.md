# update Command

[![中文](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](../zh/update.zh.md)
[![pack command](https://img.shields.io/badge/docs-pack-blue)](pack.en.md)

`update` scans existing Go spec packages, checks the latest upstream version, and can update the spec version.

## Usage

```bash
go2spec update [options] <SPECS_DIR>
```

`<SPECS_DIR>` must contain `go-*` subdirectories with spec files.

```text
SPECS_DIR/
go-golang-x-net/
go-golang-x-net/go-golang-x-net.spec
```

## Options

```text
-n
    Dry run, detect updates only, do not modify files

-j <num>
    Number of concurrent checks, default is 3
```

## Aggregate Specs Are Unsupported

The legacy updater only updates one `Version` and `Source0`. It must not update
independent-release aggregate specs. If any spec in the scanned `go-*/*.spec`
set contains a literal `%global go2spec_combine ...` or
`%define go2spec_combine ...` definition, the command reports `unsupported` and
returns nonzero before any network requests or file updates. This also applies
to `-n` (dry run), and the whole scan is rejected even when ordinary specs are
present alongside the aggregate spec.

The presence of the definition is checked, not its value: `0`, an empty value,
or a definition inside a conditional still causes rejection. No RPM macros,
shell, Lua or conditionals are evaluated. A commented-out example such as
`# %global go2spec_combine 1` is not a marker. Direct update entry points also
refuse marked files. Specs without the marker retain the existing behavior.
Do not remove the marker to bypass this protection; aggregate-aware updating
is not implemented.

## Recommended Workflow

Start with dry run mode:

```bash
go2spec update -n /path/to/SPECS
```

Apply updates after reviewing the output:

```bash
go2spec update -j 4 /path/to/SPECS
```

Review the generated diff before committing packaging changes.
