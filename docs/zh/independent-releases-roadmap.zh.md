# go2spec：从 upstream 基线到多 module 独立版本打包的 Roadmap

日期：2026-10-22

## 1. 目标

选择若干 `module@version`，go2spec 为每个 module 准备对应版本的真实源码，生成一个**可理解、可审查、可离线构建和持续维护的 library RPM spec**；构建时由宏根据实际安装的 package 自动生成正确版本的 `go(import/path)`。

最终还要能够查询目标发行版仓库、辅助选择所需 module，并安全更新已有多 module 包。但这些增强不应阻塞一个边界清楚的最小可用版本，也不能被提前描述为已经实现。

**路线原则：先把显式选择的包做对、做简单，再减少人工选择；不从“全自动打包整个仓库”开始。**

## 2. 基线、当前状态与最终目标

### 2.1 固定的开发基线

- 仓库：`go2spec-independent-releases`。
- 分支：`feature/independent-module-releases-stage`。
- 开发基线：`c4f9ae00660978a7a2dc8325cf1b19f173b8939f`。
- 本文中的 upstream 基线指上述 commit，不声称它仍是远端最新版本。
- 当前阶段功能仍是本地未提交改动，不等于 upstream 已合并、已发布或发行版已上线。



## 3. 目标工作流与职责边界

### 3.1 维护者输入什么

```bash
go2spec pack \
  -combine example.org/sdk \
  -package_name go-example-sdk \
  -package_version 20261022 \
  example.org/sdk/a@v1.2.3 \
  example.org/sdk/b/v2@v2.0.0
```

各行含义如下（示例路径需替换为实际 module 路径）：

- `go2spec pack`：运行打包生成命令，准备 spec 和源码文件，不是在本机直接构建或安装 RPM。
- `-combine example.org/sdk`：开启多 module 合并打包，把后面的两个 module 放进同一个 RPM。`example.org/sdk` 是共同的 import 路径根，用于组织安装目录和扫描自动 Provides；它本身可以没有 Go package，也不表示把这个路径下的所有 module 都选进来。
- `-package_name go-example-sdk`：指定生成的 RPM 包名，对应 spec 的 `Name`，也是输出目录名。
- `-package_version 20261022`：指定整个 RPM 的 `Version`。这里用日期作为示例；它不表示各 module 的上游版本，也不会把下面的 `1.2.3`、`2.0.0` 都改成日期。
- `example.org/sdk/a@v1.2.3`：选入 module `example.org/sdk/a`，使用其 `v1.2.3` 版本对应的源码。该 module 下实际生成能力的 package 使用 RPM 版本 `1.2.3`。
- `example.org/sdk/b/v2@v2.0.0`：选入另一个 module `example.org/sdk/b/v2`，使用其 `v2.0.0` 版本对应的源码。路径中的 `/v2` 是这个 module 身份的一部分，符合 Go 对 v2 及以上主版本的一般路径要求；它的 package 使用 RPM 版本 `2.0.0`。

每行末尾的 `\` 是 shell 的续行符，表示这些行合起来是一条命令。选项放在两个 `module@version` 参数之前。

这条命令的结果是**一个名为 `go-example-sdk`、Version 为 `20261022` 的 RPM 的打包材料**：一个 spec 加两份独立版本的源码 ZIP，而不是两个 RPM，也不是让两个 module 共用同一份仓库快照。实际 RPM 仍需在构建环境中生成。

### 3.2 go2spec 负责什么

1. 严格检查请求，拒绝不完整版本、重复 module、路径冲突和不支持的选项组合。
2. 使用 Go 官方命令准备所选版本，核对 module 身份、源码 ZIP 和实际 hash。
3. 区分 canonical Go version、module 的 RPM 版本和整个 RPM 的 Version。
4. 检查许可证、工具链要求、源码布局及 imports。
5. 生成 spec 和相应 Source 文件。
6. 后续查询明确指定的目标仓库，并给出可解释的复用或缺失判断。

### 3.3 宏和 RPM 构建负责什么

构建阶段使用已准备的固定 Source，不联网选择版本或下载 module。安装全部源码后调用一次 `%go_generate_provides`，再由 `.goipath`、file attributes 和 generator 把能力写入实际 owning binary RPM。

- `go_submodules` 继续使用 `PATH VERSION` 两列。
- 一个 package 使用所属 module 的版本，不自动使用顶层 RPM 日期版本。
- 根路径没有 package 时不虚构 root capability。
- 不由 go2spec 静态枚举普通 package 的全部 `Provides`。
- 自动 Provides 不等于自动完成 BuildRequires、Requires 或依赖兼容性证明。

## 4. 已经走到哪里

### P0：基线与显式接口——已实现，尚未上游合并

已固定开发基线，新增独立的 `-combine` 入口，保留原模式。内部以普通 Go struct 保存计划，不要求维护者另填 registry 或 lock manifest。

### P1：固定版本与独立 Source——已实现，已有真实生成验证

已实现精确下载、返回身份与 `go.mod` 检查、ZIP 安全检查、原始 ZIP SHA256、确定性 Source 编号、版本转换及失败清理。

公开验证包括 `x/sys@v0.29.0` 与 `x/term@v0.28.0`，以及 Azure `azcore@v1.17.0` 与 `azidentity@v1.8.1`。这些证明对应输入能够实际取得源码并生成结果，不证明完整上游测试或 RPM 已构建通过。

### P2：多 Source spec 与自动 Provides 接入——代码已实现，原生验收未完成

已生成版本表、逐 Source 校验与解包、许可证隔离、统一安装、自动 Provides 调用和 GOPATH 测试步骤。

依赖分析已区分实际交付的库与外部 package，保留完整 import path；未选入的独立 nested module 不按同仓库前缀误删。旧 updater 对合并 spec 直接拒绝，包括 dry run。

已有父/子 module fixture、真实 Go 样本测试和 `tests/native-combine.sh`，但**没有完成真实 openRuyi RPM 构建、payload、marker 和 header 成功路径**，因此不能把 P2 整体标为验收完成。

### 最近一次已记录验证

2026-10-19 的证据记录为：94 个顶层测试、含子测试 389 条 pass，完整 race suite 通过，build、vet、格式与脚本检查通过。这是该次状态的记录，不是本文编写时重新执行的测试结果。

## 5. 接下来的第一优先级：收敛 P2，而不是继续堆功能

### 5.1 P2-A：让生成的 spec 足够简单

**目标不是行数最少，而是维护者能直接回答：打了什么、版本从哪来、源码装到哪、依赖是什么、如何更新。**

当前 Azure spec 的冗长有两种来源，应分别处理：

- 必要信息：外部依赖列表、各 module 的版本、固定 Source、许可证归属。这些不能为了好看而删除或合并成错误依赖。
- 重复机制：每个 module 都展开的校验、解包、复制步骤，以及与 BuildSystem 的衔接。这些应优先收敛。

实施顺序：

1. 先核对 `golangmodules` 现有的阶段和扩展点，明确哪些可复用，哪些确实需要自定义；不同时保留默认和自定义的重复安装/生成。
2. 保留一份明确的 module 版本表、稳定 Source 编号和可追溯的版本/来源说明，不重复维护等价身份信息。
3. 先在生成器内部减少重复逻辑、稳定输出格式与分组，再评估生成文本是否需要共用 helper。
4. 只有在重复机制确实无法靠现有接口清楚表达时，才另提小型宏增强；单独说明接口、版本依赖与测试，不在本阶段擅改宏仓库。
5. 不把复杂性藏进不透明脚本、巨型循环或新的必填 manifest，也不为了简短去掉 hash、许可材料或测试。

验收标准：

- 两个 module 的示例能直接读懂，不需要追踪开发机路径或临时状态。
- 新增第三个 module 只增加必要信息及步骤，不复制一套新的特殊处理逻辑。
- 相同输入重复生成没有无意义 diff；输入参数调换不影响输出。
- 精简前后 Source、最终源码、许可材料、依赖和能力保持一致。
- 宏改动若被证明必要，必须另行审查，不能成为未经验证的隐藏前提。

### 5.2 P2-B：补齐真正的原生验收

先取得实际 openRuyi 构建环境及满足依赖的已安装宏，再运行已有 fixture 验收；不能拿源码加载宏或本地 shell 布局测试替代。

最后一次环境核对在 2026-10-19：宿主为 Ubuntu；官方宏 tag 只有 v0.1/v0.2，相关 v0.3 PR 尚未合并。该结果需要执行时重新确认，不能视为永久状态。`0.3~rc2 < 0.3`，不得改名、降低 spec 要求或使用 `--nodeps` 绕过。

成功条件必须同时包含：

1. 原样生成的 spec 真实通过 `rpmbuild -ba`，正常执行 `%check`。
2. 恰好生成预期 owning noarch binary RPM。
3. 父 module、nested module 及其子 package 的 capability 版本分别正确；`submarine` 不误匹配 `sub`。
4. 无 root、command、example、test-only 的虚构能力。
5. payload marker 的集合与实际字节正确，完整安装源码与 Source 逐文件一致，无重复树、覆盖或遗漏。
6. 两份许可证分别保留，样本测试真实通过，输入未被构建过程改写。

先做小型受控样本，再做真实公开 module；最后才扩大 Azure 试点。Azure 的生成 smoke 已通过，不意味着 Azure 原生试点已经完成。

### 5.3 P2-C：发布一个边界清楚的最小版本

首版可提供“显式选择多个固定版本并生成 library 源码 RPM”的能力，不必等待 P3/P4。

前提是 P2-A/P2-B 通过、旧流程回归通过、文档准确，并明确说明：不自动选择 module，不证明目标仓库依赖闭包，合并 spec 暂不能由旧 updater 更新。

把“拒绝不安全更新”作为首版安全边界，而不是把它宣传成已经具备完整维护闭环。首版不能继续让用户误以为生成成功就能直接提交发行版。

## 6. P3：目标仓库查询——从“列依赖”到“知道缺什么”

当前已经具备源码 imports 分析；P3 的新增核心是**目标仓库事实与可解释的判断**，不是重做一套重复分析器。

### 工作内容

1. 显式指定发行版仓库、repository 和架构，不默认查询开发机 rpmdb。
2. 按发行版信任策略取得并验证 repository metadata，限制下载/解压规模，保留查询快照身份。
3. 查询实际需求的 `go(package/path)` 与 owning binary RPM，不用 module 根路径是否存在来判断整个 module 是否已打包。
4. 分别报告可复用、已有但版本不足、缺失、多 provider/冲突、无法确认或查询失败。
5. 保留 canonical Go 版本与 RPM 版本语义边界；日期或 snapshot capability 不足以还原 Go 版本时，明确返回无法确认。
6. 不把“路径和版本看起来满足”当作 API 兼容证明，不把查询错误变成自动新建包。

### 验收

冻结 metadata fixture 覆盖架构差异、无 root package、多 provider、损坏/缺失 metadata 和不可比较版本；同一目标快照在不同宿主上结果一致。再用真实目标仓库验证诊断。

本阶段先做诊断和复用建议，不自动修改发行版包集合，也不擅自把用户指定版本替换为仓库较新版本。

## 7. P4：辅助选择与安全更新——分成两个独立增量

### P4-A：从消费者出发选择 module

- 先有明确消费者与固定 revision，再在准备阶段用受控 Go module-aware 查询取得依赖信息，结合实际 imports、目标平台与测试范围选择。
- 发现列表不等于交付列表；不默认打包整个 monorepo。
- 先显示选择结果及理由，再生成确定的打包计划。`all` 若实现，必须显式启用。
- 不把一次消费者的需求当成共享 RPM 的全部交付范围，不自动移除其他消费者仍可能使用的 module。
- `replace`、`exclude`、retract 与非标准来源逐项定义支持政策；当前不支持的情况仍明确拒绝，不在这里一次承诺通吃。

验收：只需要 A 的消费者不默认选入全仓；版本意图不被暗改；内部/外部边界、工具链变化和规模可见。application 输出如纳入新能力，单独验收，不继承 library 模式的通过结论。

### P4-B：逐 module 更新

- 识别生成的多 module spec，明确本次要更新、添加或移除哪些 module。
- 同步修改对应 Source、hash、canonical/RPM 版本、版本表、许可证、依赖和安装步骤。
- 更新 A 不意外更新 B；移除 B 必须显式提出，并展示失去的能力及反向依赖影响。
- 保留人工段落、补丁和其他文件；不能安全处理时输出差异或拒绝覆盖，不执行未知 spec 宏来猜结构。
- 先完整准备和验证新结果，再发布更新；区分无更新、不支持、冲突和失败。
- 延续已有 RPM Version/Release 维护政策，不能随便挑一个 module 版本作为整个 RPM 版本；同日变更也必须能够按发行版规则区分。

验收：单项更新、增删 module、第二项失败、手工修改、已有输出与旧单 module updater 均有回归测试。支持完成前继续保留当前拒绝保护。

## 8. 上游审查、发布与推广

按可独立审查的行为拆分，而不是把所有新能力放成一个大 PR：

1. **请求、版本与 Source 链路**：严格身份、版本转换、ZIP/hash、失败契约，随附相关测试。
2. **多 Source spec 与依赖分析**：包含许可证、安装、版本表、旧 updater 保护、可读性和真实原生证据。
3. **首版文档和发布说明**：真实支持边界、宏下限、Source 交付方式、人工审查要求及已知限制；不把 P3/P4 写成已有功能。
4. **后续 P3/P4 增量**：分别审查仓库查询、辅助选择、安全更新，每个功能携带自己的测试和文档。

接口相互依赖时可以作为明确的 PR 栈，不为强行拆分而合并不可用的中间状态。具体提交与推送仍需授权；本文不执行发布。

推广顺序：受控原生样本 → 小型真实包 → 少量人工审查的发行版试点 → 反向依赖检查 → 扩大范围。试点覆盖发行版要求的 x86_64、riscv64 和 rva20 构建目标；源码分析只覆盖 amd64/riscv64 不能代替 rva20 实际构建。

每次试点保留生成器版本、输入、Source hash、spec、已安装宏身份、仓库/架构、完整构建日志、RPM 及 payload/header 审计。历史宏迁移的构建数量和失败延期决定不能冒充本次 go2spec 验收。

## 9. Source 交付、版本与回退政策

### Source 交付

当前交付物是 spec 加固定 module ZIP，`%prep` 校验原始 ZIP SHA256；不是 Go `h1:`，也没有自动生成 RemoteAsset URL/OBS service。

首版先把这条已有交付路径在目标构建服务验证清楚。自动来源服务另做增量，必须绑定同一份真实资产，不猜 URL、不暴露凭据、不引用开发机 cache。模块 ZIP 缺必要构建材料时，应另有明确 Source 方案或报告不支持，不能跳测试凑成功。

### 版本政策

- module 的 canonical Go version 与转换后的 RPM version 分开保存。
- pseudo-version 的 snapshot 日期使用打包日期，不用上游 commit 时间冒充。
- Go 到 RPM 的转换不是普遍无损、保序的替代，provider 判断不能只按转换后的字符串取最大值。
- 整个 RPM 的 Version 独立维护，不由任意 SourceN 决定。

### 回退

生成器发布和发行版打包提交分开管理。降级工具不会撤销已经发布的包；回退 spec 必须连同 Source、版本表与依赖一起考虑。已有消费者依赖新能力时，应审查反向依赖，必要时前进修复，不能移动 tag、覆盖归档或仅改顶层 Version。

## 10. 不变的工程边界

- 保留 `.goipath`、`go.attr`、`go-rpm-integration`、`%go_generate_provides` 和两列 `go_submodules`，不为了新功能另造整套名称。
- 不强制 registry、重复 manifest、每 module 一个 subpackage 或同路径多版本共存。
- 不在 RPM 构建阶段下载 module 或自动下载工具链。
- 不用 go.mod 证明自身 release；它声明身份，不声明 module 自身发布版本。
- 不因同仓库前缀就把未交付源码当成内部依赖。
- 不修改、删除或跳过测试，也不改依赖掩盖失败；本地测试通过、源码准备通过和原生构建通过分别记录。
- 新模式通过不等于旧生成器的 Source/hash/依赖等历史问题全部修复，旧流程后续改造另有回归范围。
- 不自动删除发行版手写 Provides、不重写全仓、不改生产 OBS、不自动 commit/push。

## 11. 两个里程碑，而不是一个模糊的“完成”

### 里程碑一：首版可试用

- [x] 显式选择多个独立版本，保留旧 CLI 语义。
- [x] 各版本与独立 Source、实际字节 hash、完整版本表绑定。
- [x] 许可证、完整 import path、安装冲突和失败输出有保护。
- [x] 旧 updater 拒绝不支持的合并 spec。
- [x] 已有本地完整 suite/race 及固定公开版本生成验证记录。
- [ ] spec 可读性收敛并通过维护性审查。
- [ ] 满足版本要求的真实 openRuyi 环境完成原样 RPM 构建和完整产物审计。
- [ ] 完成小型真实试点、旧功能回归及 Source 交付验证。
- [ ] 上游审查、发布说明与明确的试用边界完成。

### 里程碑二：达到完整维护目标

- [ ] 能查询明确目标仓库，可靠区分缺失、冲突与无法确认。
- [ ] 能从明确消费者辅助选择 module，而不是默认打包全仓。
- [ ] 能安全逐 module 更新，不丢手工内容、不无意增删交付范围。
- [ ] 目标架构试点、反向依赖检查、渐进推广与回退流程通过。

**当前最重要的下一站是“简化并验收 P2”，不是先做全 Azure 自动选择，也不是先写完整依赖求解器。** 首版把“明确选择 → 正确 Source → 可读 spec → 真实 RPM”打通；之后再把它扩展为“辅助判断 → 安全选择 → 持续更新”。

### 2026-10-22：P2-A 局部精简与回归结果

- `go_submodules` 直接写各 module 的 RPM 版本，去掉仅引用一次的 `ver_moduleN`；canonical Go version 仍保留在 Source 注释和 module 清单中。
- `%prep` 集中校验全部 ZIP，全部通过才开始解包；许可证使用 `install -D -p -m 0644` 复制，保留独立目录和嵌套许可材料。不新增宏或隐藏脚本。
- 显式安装和测试阶段仍保留：现有宏默认按单源码树复制当前目录，不能直接用于这一多 Source 布局。宏源码核对不等于 BuildSystem 的原生集成已经验证。
- 两个和三个 module 的完整源码树、目录、字节、许可证、参数顺序稳定性及任意 Source 篡改拒绝测试通过。完整 suite 和 race 各为 94 个顶层测试、396 条 pass，无 fail/skip；build、vet 通过。
- 相同固定版本的 Azure CLI 重新生成成功，两份原始 ZIP SHA256、module 版本、21 项外部 Go BuildRequires 与 13 项 Go Requires 不变；211 个源码文件和两份独立许可证经真实 prep/install 布局核对，字节一致。未执行 Azure 上游测试集，也没有构建 Azure RPM。
- Azure 样例精简前后均为 110 行。这次减少的是间接引用和重复命令，并增加分组与集中校验；不是单纯压缩行数。维护性仍需审查，不能把局部精简等同于首版上线完成。
- 新的父/子 module fixture 已导出，四个样本测试通过。原生脚本在 Ubuntu 身份检查处返回 BLOCKED/exit 2，未进入 RPM 构建；P2-B 和原生成功路径仍未完成。

本轮证据位于 `/home/wsl/oerv/go2spec-spec-cleanup-20261022-LltP5e/`，其中 `REPORT.zh.md` 说明检查范围，`azure-validation.json` 与 `test-summary.json` 记录实际结果。未提交或推送，也未修改宏仓库和 OBS。

### 2026-10-27：阶段分支准备与基线验证

- 已从 `upstream/master` 的 `c4f9ae00660978a7a2dc8325cf1b19f173b8939f` 创建 `feature/independent-module-releases-stage`，保留候选实现，不覆盖旧分支或原始 `go2spec` 工作树。
- 当前分支完整 `go test`、race、`go build`、`go vet` 和 `git diff --check` 通过。
- `golang.org/x/sys@v0.29.0` 与 `golang.org/x/term@v0.28.0` 的真实 CLI smoke exit 0；版本表为 `0.29.0` / `0.28.0`，Source SHA256 分别为 `1d759c017d09328a4dacdd4a1a0170cf21641995c6e3ca1f96213369d73c6846` 和 `ede5a49eac0946d10051703fa19f9f05c4cac3a217e55b7313e24b6c15ade914`。
- 重新导出的父/子 fixture 生成成功，预期能力仍区分 `1.2.3` 和 `0.4.0`；原生脚本在 Ubuntu 身份检查处返回 BLOCKED/exit 2，未进入 `rpmbuild`。

本阶段仍未 commit、push、创建 upstream PR 或写入 OBS；真实 openRuyi RPM 成功路径仍是后续验收条件。