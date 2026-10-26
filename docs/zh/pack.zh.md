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

使用 `-combine` 显式开启新的 **library 源码 RPM** 模式。每个 module 都使用自己
指定的版本及对应源码，不再共用一个仓库 snapshot。不加这个选项时，多个位置参数
仍表示分别打包；原有单 module、list、program 模式不变。

```bash
# 以下是示意路径，使用时替换为真实 module 和版本。
go2spec pack \
  -combine example.org/sdk \
  -package_name go-example-sdk \
  -package_version 20261019 \
  example.org/sdk/a@v1.2.3 \
  example.org/sdk/b/v2@v2.0.0
```

与其他 Go flag 命令一样，选项放在位置参数之前。

| 选项 | 含义 |
|---|---|
| `-combine <import-root>` | 共同的安装路径和 auto-Provides 扫描根；根路径本身可以没有 Go package。 |
| `-package_name <name>` | 显式 RPM 包名，合并模式必填。 |
| `-package_version <version>` | 显式 RPM `Version`，不含 Epoch/Release，与各 module 的版本分开。 |
| `-license <expression>` | 可选：人工审查后填写、覆盖**所有**选定源码的许可证表达式；仍要求有实际许可证文件。 |

每个参数必须是完整的 `module@canonical-version`。拒绝 branch、`latest`、不带版本
的 commit、重复 module path（即使版本不同）、major 与路径不匹配、以及共同根之外
的 module。路径按 `/` 边界判断，`example.org/sdk-extra` 不属于 `example.org/sdk`。
不能混用 `-git_revision`、`-force_prerelease`、`-list`、`-program_package_name`、
`-allow_unknown_hoster` 或非 library 的 `-type`。

### 源码、版本与输出

上例输出：

```text
go-example-sdk/
  go-example-sdk.spec
  module-0-v1.2.3.zip
  module-1-v2.0.0.zip
```

Source 按 module path 排序编号，不受参数顺序影响。每份 ZIP 都是原始 Go module
归档，而不是同一 commit 的仓库 tarball。向构建服务准备源码时，应将这些固定文件
与 spec 一起交付；不生成猜测的下载 URL、带凭据的 URL、开发机 cache 路径或额外
lock manifest。生成的 `%prep` 先集中验证**全部 ZIP 原始字节的 SHA256**，全部通过后
才开始解包；这不是 Go 的 `h1:` checksum。本模式暂不自动生成 RemoteAsset URL 或源码服务配置，维护者
需要按目标构建服务的源码流程审查、交付这些文件。

工具在私有 module/cache 中执行 `go mod download -json module@version`，设置
`GOWORK=off`、`GOTOOLCHAIN=local`，保留用户的 Go 网络和校验配置。校验返回身份、
`.info`、module 声明、ZIP 中实际 `go.mod`，并拒绝可选 Origin 中冲突的 tag 或
pseudo-version hash。SHA256 只固定字节，并非独立的 release 身份认证；下载的
信任仍取决于 Go proxy/checksum 配置。

spec 分开保存 canonical Go version 与 RPM 兼容版本，并生成完整的两列
`go_submodules` 表。正式版本去掉 `v`；预发布用 `~`；pseudo-version 使用**本次
生成/打包日期**，不是上游 commit 时间。`+incompatible` 保留在 Go 身份中，不写入
RPM 版本文本。转换不是无损、对所有情况都保序的 Go 版本替代；预发布标签内部含
连字符、以及暂不支持的 snapshot revision 格式会明确失败，不偷偷改写。

全部源码验证完成后才发布输出。已有目标路径一律拒绝，包括空目录。普通错误会
清理私有临时源码，不发布成功 spec；中断后没有最终 spec 的目录不能当作完整结果。

### 许可证、依赖与构建审查

不传 `-license` 时，按每个**固定 module 版本**获取元数据，并逐一核对识别到的
许可证文件内容（仅规范 CRLF）。缺失或不一致直接报错，不写 `TODO`。只有人工审查
全部源码的许可后才能使用 `-license`，语法校验不等于法律审查。匹配 Go `GOPRIVATE`
配置的 module 必须传 `-license`，不会将其路径发送给公共元数据服务。识别 `LICENSE`、
`LICENCE`、`COPYING`、`NOTICE` 及加点、连字符、下划线的文件名变体；各 module 的
许可证放进独立子目录，避免多个 `LICENSE` 相互覆盖。

依赖分析合并 Linux `amd64`、`riscv64` 的 imports，使用本地 Go 命令的 release/tool
build tags 和基线 CPU 特性，清空自定义 `GOFLAGS`、`GOEXPERIMENT`。标准库和实际
交付的内部 library 不生成外部依赖；外部依赖保留**完整 package import path**，
形成 `go(path)`。仅测试、命令、example 使用的依赖只进入 BuildRequires；若交付
的库也需要它，则进入 Requires。被 require、但未选入本 RPM 的 nested module
仍是外部依赖，不能按同仓库前缀误删。这不是 MVS、provider 查询，也不证明目标仓库
有 API 兼容的依赖；需要对照目标 Go 版本/build tags 审查，变化后重新验证。

spec 先装齐各份源码，再调用一次 `%go_generate_provides`，不手写普通 Provides，
不因为根路径存在就虚构 root capability。显式 `%install` 替代默认单源码树安装。
`%check` 在 GOPATH 模式下执行测试、禁止 Go 自动下载 module，不跳过失败的测试；
但这些环境变量本身不会禁止上游测试代码主动访问网络。

### 首版支持边界

- ZIP 中必须有真实 `go.mod`，本地 Go 工具链必须足够新；不自动下载工具链，也不为
  legacy module 合成 `go.mod`。
- 拒绝 `replace`/`exclude`、未映射的 nested `go.mod`、父子 Source 归属冲突、源码
  自带 `.goipath`，以及无法自动推导原生依赖的活动 Cgo 文件。这些情况需手工打包。
- 仅接受保守的普通文件 ZIP 子集。链接、特殊权限、越界路径、local/central header
  不一致、改变路径或类型的 extra field 会被拒绝，避免 Go 与系统 `unzip` 解释不同。
- 目标构建环境需要 `go-rpm-macros >= 0.3`、`go`、`coreutils`、`unzip`。仍须在真实
  openRuyi 环境构建，检查测试、安装后的 marker、归属 RPM 和 header capability。
  生成成功或源码布局脚本测试通过，不等于这项验收通过。
- 暂不实现自动选择 module、目标仓库 provider 查询、完整 module graph 兼容性求解
  和逐 module 更新。[旧 updater 会拒绝带标记的合并 spec](update.zh.md#不支持聚合-spec)，
  dry run 也拒绝；不要删除 `%global go2spec_combine 1` 绕过保护。

## 真实 RPM 验收

普通 Go 测试集包含 `TestAggregateNativeFixture`：通过本地 file proxy 准备两个
独立 module ZIP，调用 pack CLI 入口生成 spec，再禁用下载运行样本自身的 Go 测试。
它不会因为没有 RPM 而跳过，但也**不声称构建出了 RPM**。

在 go2spec 源码目录执行下列命令，可把同一份样本导出给真正的 openRuyi 环境。
输出必须是尚不存在的绝对路径：

```bash
GO2SPEC_NATIVE_FIXTURE_DIR=/tmp/go2spec-native-fixture \
  go test -count=1 -run '^TestAggregateNativeFixture$' .
```

样本含 `v1.2.3` 的父 module 和 `v0.4.0` 的 nested `sub` module。聚合 RPM Version
故意使用 `99.0`，用来发现错误回退到 `%{version}` 的情况。预期能力如下：

| `example.test/go2spec/fixture` 下的 import path 后缀 | RPM 版本 |
|---|---|
| `/base` | `1.2.3` |
| `/sub` | `0.4.0` |
| `/sub/leaf` | `0.4.0` |
| `/submarine` | `1.2.3`，不能按字符串前缀误归给 `/sub` |

根路径没有 package；命令、example、test-only 目录都不应产生 capability。
父 module 的库实际导入 nested module，两个 module 都有测试。
`expected-provides.txt` 只是核对清单，不是伪造的 `.goipath` 或 RPM header。

将整个导出目录和 `tests/native-combine.sh` 放到干净的 openRuyi 构建环境中，事先
安装标准 RPM 构建配置及生成 spec 声明的 BuildRequires，再以构建用户运行：

```bash
bash tests/native-combine.sh /tmp/go2spec-native-fixture /tmp/go2spec-native-build
```

构建目录同样必须尚不存在。脚本不安装软件，不删除旧目录，失败时保留日志与产物。
它在访问 rpmdb 前先检查系统身份，然后核对已安装 go-rpm-macros 的 RPM 身份、
版本和文件完整性。`0.3~rc2` 小于 `0.3`，会被拒绝，不改名、不使用 `--nodeps`。
不从源码 `--load` 宏、不换 rpmdb、不禁用测试，也不改写 spec 的版本或依赖。

只有真实 `rpmbuild -ba` 成功、归属 noarch RPM 正确、Go capabilities 与 payload
marker 字节完全匹配、安装的源码与两份 ZIP 逐文件字节一致且没有多余源码文件、两份
许可证未相互覆盖、构建日志中四个样本测试均通过、输入 hash 未改变，才输出 PASS。退出码 `2` 表示环境前提阻塞，不是通过或跳过验收。
普通测试、脚本语法检查和拒绝路径测试，均不等于 native 成功路径已经验证。
