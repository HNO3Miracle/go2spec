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

## 多个 module 分别选版本，合成一个 RPM

使用 `-combine` 显式开启新的 *Independent releases* 模式：**各个 module 使用自己选定的正式版本和对应源码，最后组合到一个 RPM。**

每个 module 都使用自己指定的版本及对应的 Go Module Proxy Source，不再共用一个仓库 snapshot。

```bash
# 以下是示意路径，使用时替换为真实 module 和版本。
go2spec pack \
  -combine example.org/sdk \
  -url https://example.org/sdk \
  -package_name go-example-sdk \
  -package_version 20261019 \
  example.org/sdk/a@v1.2.3 \
  example.org/sdk/b/v2@v2.0.0
```

| 选项 | 含义 |
|---|---|
| `-combine <import-root>` | 共同的安装路径和 auto-Provides 扫描根；根路径本身可以没有 Go package。 |
| `-url <project-homepage>` | 上游项目主页，写入 spec 的 `URL`；跨仓库或无法自动判断时必填。 |
| `-package_name <name>` | 显式 RPM 包名，合并模式必填。 |
| `-package_version <version>` | 显式 RPM `Version`，不含 Epoch/Release，与各 module 的版本分开。 |
| `-license <expression>` | 可选：人工审查后填写、覆盖**所有**选定源码的许可证表达式；仍要求有实际许可证文件。 |

在决定 `module@version` 输入前，维护者可以先分析一个明确固定版本的项目：

```bash
go2spec module-select --format text github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

此参数会进行依赖分析，辅助进行 module 选择。详情见 [Module Select 文档](module-select.zh.md)。审阅每个候选的使用依据和 Go 选定版本后，再人工将最终选择的 Source 显式传给 `pack -combine`。

每个参数必须是完整的 `module@canonical-version`。拒绝 branch、`latest`、不带版本的 commit、重复 module path（即使版本不同）、major 与路径不匹配、以及共同根之外的 module。



### 许可证、依赖与构建审查

不传 `-license` 时，按每个**固定 module 版本**获取元数据，并逐一核对识别到的许可证文件内容（仅规范 CRLF）。缺失或不一致直接报错。只有人工审查全部源码的许可后才能使用 `-license`。识别 `LICENSE`、`LICENCE`、`COPYING`、`NOTICE` 及加点、连字符、下划线的文件名变体；各 module 的许可证放进独立子目录，避免多个 `LICENSE` 相互覆盖。

依赖分析分别扫描 Linux `amd64`、`riscv64` 的 imports，使用本地 Go 命令的 release/toolbuild tags 和基线 CPU 特性，清空自定义 `GOFLAGS`、`GOEXPERIMENT`。生成 noarch 合并包时，两架构的 library package、BuildRequires 和 Requires 集合必须一致；架构差异会明确失败。仅测试、命令、example 使用的依赖只进入 BuildRequires；若交付的库也需要它，则进入 Requires。


