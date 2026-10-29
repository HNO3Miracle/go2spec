# go2spec 多 Module 独立版本打包：阶段任务逐点说明

日期：2026-10-28

本文逐项回应“同一 RPM 合并多个独立版本 Go Module”的阶段任务，说明实现入口、设计取舍、测试和真实 OBS 验收证据。状态区分为：**已实现并验证**、**已实现但仍有限制**、**后续范围**。生成 spec 仍须维护者审查；本文不把计划或本地测试描述成已经发布到官方发行版。

## 1. 多 Module 合并打包

### 1.1 新增独立 `-combine` 模式

已新增 opt-in 命令模式：

```bash
go2spec pack \
  -combine github.com/Azure/azure-sdk-for-go/sdk \
  -url https://github.com/Azure/azure-sdk-for-go \
  -package_name go-azure-sdk-combine-smoke \
  -package_version 20261019 \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1 \
  github.com/Azure/azure-sdk-for-go/sdk/azcore@v1.17.0
```

参数解释：

- `-combine <import-root>`：明确共同的安装路径和自动 Provides 扫描根。它可以没有根 package，不表示自动选入该路径下的所有 Module。
- `-package_name`：最终 RPM 的包名，对应 spec 的 `Name`。
- `-package_version`：最终聚合 RPM 的 `Version`，与选中 Module 各自版本独立。
- `-url`：上游项目主页，写入 spec 的 `URL`；跨仓库组合或无法可靠推导主页时必填，不是 Source 下载地址。
- `module@version`：每个位置参数明确选择一个 Module 和它的 canonical Go 版本。这里 `azcore` 和 `azidentity` 是两个独立 Module，版本分别为 `v1.17.0` 和 `v1.8.1`。
- `-license <expression>`：可选；只在维护者审查所有选中 Source 的许可证后使用。即使传入该选项，实际许可证文件仍然必须存在。

### 1.2 输入、路径与版本检查

合并模式要求完整的 `module@canonical-version`，并检查：

- Module path 和 Go module path-major 规则；
- Module 必须在共同 import root 下，且按 `/` 路径边界判断；
- 拒绝 `latest`、branch、未固定 revision、不完整或非 canonical 版本；
- 拒绝同一路径重复选择（即使版本不同）；
- 拒绝 RPM 包名、RPM Version、许可证表达式中的宏/换行等不安全内容；
- 拒绝与旧 git revision、list 或 program 选项互相冲突的参数组合。

## 2. 源码准备与 spec 生成

go2spec 在本地临时下载源码用于身份、许可证、依赖和 ZIP 安全校验；正式成功输出只包含 spec，不要求把 Module ZIP 提交到仓库。每个 Source 使用固定 Go Module Proxy URL 和紧邻的 `#!RemoteAsset: sha256:...`，例如：

```spec
URL:            https://github.com/Azure/azure-sdk-for-go
#!RemoteAsset:  sha256:<azcore-zip-sha256>
Source0:        https://proxy.golang.org/github.com/!azure/azure-sdk-for-go/sdk/azcore/@v/v1.17.0.zip#/module-0-v1.17.0.zip
```

OBS source service 在源码准备阶段下载并校验 Source；RPM 构建阶段不联网。构建时将所有选中的源码放入最终 GOPATH 布局后调用一次 `%go_generate_provides`。由于现有 `golangmodules` 默认安装流程假定单一源码树，目前合并 spec 保留显式安装和 `%check`。

真实公开 Module 生成 smoke 的 Azure spec：

- [生成的 spec](../../go2spec-native-checks-20261019-wzsn0V/azure-smoke/go-azure-sdk-combine-smoke/go-azure-sdk-combine-smoke.spec)
- spec 包含 21 项 Go `BuildRequires` 和 13 项 Go `Requires`；未选入、但被独立 require 的 `azidentity/cache` 被保留为外部 BuildRequires，而不是按同仓库前缀误认为内部源码。

注意：Azure 的证据证明固定公开版本可真实下载并生成 spec，不证明 Azure 上游测试集或 Azure RPM 已构建。真实 OBS RPM 验收使用下述受控父/子 Module fixture。

## 3. Go Package Provides 与依赖

### 3.1 版本化 Provides

spec 使用现有宏协议：

```spec
%global go_import_path example.test/go2spec/fixture
%global go_submodules %{expand:
example.test/go2spec/fixture 1.2.3
example.test/go2spec/fixture/sub 0.4.0
}
```

安装完整源码布局后由 `%go_generate_provides` 在实际 package 目录写入 `.goipath`；file attribute 和 integration helper 再生成 RPM header 的 `go(import/path)`。根路径没有可导入 package 时不生成虚构 root capability；不由生成器静态枚举普通 Provides，也不直接拿聚合 RPM `Version` 代替 Module 版本。

### 3.2 嵌套 Module 与 package 边界

自动依赖分析基于各个选定版本的源码 package/import，而不是仅把所有 `go.mod require` 机械生成成 Requires，也不是把相同仓库前缀都当成本包内部源码。

当前实现区分：

- 实际交付、可导入的内部 library package；
- 未选择的独立 nested module（作为外部依赖）；
- 仅测试、命令或 example 引入的 build-only 依赖；
- library 运行闭包需要的外部 runtime 依赖。

依赖项保留完整 package import path。分析覆盖 Linux `amd64` 和 `riscv64` imports；生成 noarch 合并包时，两架构的 library package、BuildRequires 和 Requires 集合必须一致，架构差异会明确失败，不会把 arch-only provider 无条件写入。使用本地 Go 命令的 release/tool tags 和 baseline CPU 特性；这不是完整 MVS/provider/API 兼容性求解，也不代表所有目标架构都已完成 RPM 构建。

父/子 native fixture 特别验证：

| import path | Module 版本 | RPM header 中的 capability |
|---|---:|---|
| `example.test/go2spec/fixture/base` | `1.2.3` | `go(.../base) = 1.2.3` |
| `example.test/go2spec/fixture/sub` | `0.4.0` | `go(.../sub) = 0.4.0` |
| `example.test/go2spec/fixture/sub/leaf` | `0.4.0` | `go(.../sub/leaf) = 0.4.0` |
| `example.test/go2spec/fixture/submarine` | `1.2.3` | `go(.../submarine) = 1.2.3` |

fixture 的聚合 RPM Version 故意设为 `99.0`。nested Module 根目录有 `go.mod` 但没有根 package 时，不虚构该 Module root capability；实际 `leaf` package 使用 nested Module 版本。`submarine` 用于检查 slash-boundary，而非普通字符串前缀匹配。命令、example、test-only 目录都不应获得 package capability。

### 3.3 更新操作的边界

自动 Module 选择、目标仓库 provider 查询、完整 module graph 兼容性求解和多 Module updater 不属于本阶段。现有 updater 只理解单个 Version/Source0，所以对字面 `%global go2spec_combine` / `%define go2spec_combine` spec 明确拒绝更新，包括 dry run；不会只改 Source0 或聚合 Version 造成损坏。不要删除标记绕过保护。

## 4. 测试与真实 RPM 构建验证

### 4.1 自动化测试

阶段分支基于 upstream commit `c4f9ae00660978a7a2dc8325cf1b19f173b8939f`。截至本阶段验证记录，以下检查均通过：

```text
go test -count=1 ./...
go test -race -count=1 ./...
go build ./...
go vet ./...
gofmt
git diff --check
```

该次完整 Go suite 为 94 个顶层测试、396 条 pass 事件，0 fail/skip。测试覆盖请求校验、canonical/major 版本、pseudo-version 转换、Source 身份与 hash、ZIP 越界/特殊条目、license metadata、依赖分类、内部/外部 Module、spec 生成稳定性、输出冲突、失败清理和 updater 拒绝。

固定公开版本 smoke 已执行：`golang.org/x/sys@v0.29.0` 和 `golang.org/x/term@v0.28.0` 分别生成固定 Proxy Source URL、RemoteAsset hash 和版本映射；正式 CLI 输出目录只包含 spec。该 smoke 是源码准备/spec 生成验证，不是这两个公开 Module 的 native RPM 构建。

### 4.2 真实 OBS native fixture

受控 fixture 已提交到个人 OBS 测试项目：

```text
项目：home:HNO3Miracle:go-auto-provides
包：go-go2spec-combine-fixture
revision：1
```

包由 OBS 正常 `rpmbuild -ba` 构建，且正常执行 `%check`。两个目标都成功：

- `amd64/x86_64`：succeeded / published；
- `riscv64/riscv64`：succeeded / published。

OBS 日志明确显示安装 `go-rpm-macros-0.3-8.1.nor`、两份 Source SHA256 校验通过、`%go_generate_provides` 生成四个 marker、四个 fixture Go 测试全部通过，随后写出 noarch RPM。

产物均为：

```text
go-go2spec-combine-fixture-99.0-1.1.nor.noarch.rpm
```

独立产物审计逐项检查了 RPM header 的四个 `go(...)` Provides、RPM Name/Version/Release/Arch、marker 集合和 marker 字节、14 个源码文件、两份独立 LICENSE 副本；源码和许可证与 OBS 下载的两份 Source 原始成员逐字节比较通过，未发现多余或遗漏文件。该 fixture revision 仍是旧的手工 ZIP 输入回归，不能代替新的 spec-only Source service 验收。

证据文件：

- [完整 OBS 验收报告](../../../obs-fixture-20261027-parent/evidence/REPORT.zh.md)
- [x86_64 OBS 构建日志](../../../obs-fixture-20261027-parent/evidence/x86_64/build.log)
- [riscv64 OBS 构建日志](../../../obs-fixture-20261027-parent/evidence/riscv64/build.log)
- [payload 与 marker 审计结果](../../../obs-fixture-20261027-parent/evidence/payload-audit.json)
- [完整 payload 清单审计](../../../obs-fixture-20261027-parent/evidence/payload-inventory-audit.json)

这是个人 OBS 上的阶段性真实构建验证，不等于官方发行仓库发布。宏 `0.3-8.1` 是用于测试的个人 OBS 构建，源码固定到宏 PR #2 的 commit；宏 PR 的合并与官方发布由社区维护者负责。

## 5. 预期交付、限制与后续

当前代码已在个人 fork 阶段分支并提交 upstream PR：

- 分支：`feature/independent-module-releases-stage`
- 提交：`7a2a3fc1b524966ee491ed050eaef007baedb47d`
- PR：[openRuyi-Project/go2spec#1](https://github.com/openRuyi-Project/go2spec/pull/1)
- PR 状态需以 GitHub 当前页面为准；本说明记录的是阶段成果，不表示 PR 已合并。

此次范围是：维护者明确选择多个固定 Module 版本，go2spec 分别准备真实 Source，生成可审查 spec，RPM 构建后得到版本正确的 package Provides。未实现的 provider 查询、自动 Module 选择、完整 MVS/API 兼容性证明和多 Module updater 均已在 CLI/文档中明确限制。

仍需在 review 中重点讨论：spec 的维护性、Source 向目标构建服务的交付方式、正式 `go-rpm-macros >= 0.3` 发布时机，以及公开 Module 示例是否需要进一步做完整 OBS RPM 试点。任何新 package 仍需维护者审查生成 spec、依赖和许可证，不应把自动生成等同于可直接发布。

## 6. 方案取舍与可解释性

实现采用显式选择而非默认扫描整个仓库，原因是发现的 Module 集合不等于发行版应该交付的集合；每个上游版本、源码和 RPM capability 需要一一对应。Source 采用固定 ZIP 和原始字节 hash，是为了令构建阶段离线、可复现且不依赖开发机缓存。聚合 RPM Version 与 Module 版本分离，是为了避免日期/包版本错误传播到 Go dependency capability。

本阶段优先保证边界可见和失败安全，而不是追求“全自动”：不支持的输入明确拒绝，updater 暂时拒绝合并 spec，native 验收只在具有实际宏 RPM 的 OBS 环境执行。维护者可以据此逐项 review 版本绑定、Source、`go_submodules`、依赖分类和 RPM 产物。
