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
