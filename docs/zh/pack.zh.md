# pack 命令

[![English](https://img.shields.io/badge/docs-English-blue)](../en/pack.en.md)
[![update 命令](https://img.shields.io/badge/docs-update-blue)](update.zh.md)

`pack` 根据 Go import path 生成 RPM spec 初稿和源码归档。

**该命令用于生成可继续维护的起点，不是最终可直接提交的包，提交到 openRuyi 或用于构建前必须人工 review 生成的 spec。**

## 用法

```bash
go2spec pack [FLAG]... <go-package-importpath>
```

示例：

```bash
go2spec pack golang.org/x/net
```

## 参数

```text
-git_revision <rev>
    checkout 指定 git revision，例如 tag、branch 或 commit

-force_prerelease
    打包 HEAD 或 tip，而不是优先选择最新 tag

-type <type>
    选择包类型，支持 library、program、library+program 和 program+library

-program_package_name <name>
    覆盖程序包名，适用时也覆盖源包名

-allow_unknown_hoster
    允许未知 hoster 参与包名生成，生成结果必须人工检查
```

## 输出

执行成功后会在当前目录创建：

- `<package-name>/`，package 工作目录
- `<package-name>/<package-name>.spec`，生成的 RPM spec 初稿
- `<package-name>_<version>.orig.tar.<gz|xz>`，上游源码归档

如果目标 package 目录已存在且非空，go2spec 会停止执行，以避免覆盖已有工作。

## 包类型

- `library`，适合 Go library module
- `program`，适合根 package 为 `main` 的命令包
- `library+program`，适合同时发布库文件和命令子包的项目
- `program+library`，适合以程序为主，同时需要库子包的项目

不传 `-type` 时，go2spec 会根据 pkg.go.dev package 元数据和本地 `go list` 输出推断类型。
