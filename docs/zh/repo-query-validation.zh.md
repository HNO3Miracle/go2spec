# 第二阶段真实仓库查询记录

复验日期：2026-11-03

本记录对应个人分支上的 `repo-query` 实现，不是 openRuyi 官方发布声明。以下查询使用维护者已信任的 openRuyi OBS keyring，并要求验证仓库签名；程序没有读取开发机 rpmdb。

## 查询命令

以下示例需要使用从 openRuyi 仓库配置中取得、且已由维护者信任的本地 keyring：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --gpg-keyring /path/to/trusted-openruyi-keyring \
  --require-signature \
  --require 'go(aead.dev/mem)>=0:0.2.0' \
  --require 'go(example.test/missing)>=0:1' \
  --format json
```

riscv64 查询使用相同选项，将 URL 改为 `https://repo.build.openruyi.cn/openruyi/riscv64/`，并将 `--arch` 改为 `riscv64`。未自动下载或信任远程 key。

## 实际结果

| 仓库 / 架构 | repomd revision | repomd SHA256 | Signature | `go(aead.dev/mem)>=0:0.2.0` | `go(example.test/missing)` |
|---|---:|---|---|---|---|
| openruyi / x86_64 | `1793671566` | `75d3a28364e1e132388cdad10f0347ed71247c773a7885295df5abb482594f60` | verified | satisfied | missing |
| openruyi / riscv64 | `1793673044` | `535da625f8289c06b34374ec2c228f4f971aa84ddb10fdb0e9511eceac032f5f` | verified | satisfied | missing |

两个快照均由 fingerprint `51101B1408F35F75BE9C12807447DA4C8D2E5D3A` 验证。`go(aead.dev/mem)` 的 `EQ` Provide 声明了版本 `0.2.0`，未声明 RPM Release；所以查询要求刻意不含 Release。x86_64 provider 是 `go-aead-dev-mem.noarch`，包 EVR `0:0.2.0-3.28.or`；riscv64 provider 包 EVR 为 `0:0.2.0-3.19.or`。`go(example.test/missing)` 在两个指定快照中均无精确 provider。

Primary compressed/open checksum、大小、package 数量、signature 状态，以及每条 dependency/provider 的详细结果保存在可公开审查的 JSON 文件中：

- [x86_64 查询报告](repo-query-openruyi-2026-11-03-x86_64.json) — SHA256 `f47547654107d954c5a5c8a78c8615f2d7a221a20c9924a37056d9914e50667c`
- [riscv64 查询报告](repo-query-openruyi-2026-11-03-riscv64.json) — SHA256 `ec50602280135a4d52b821b0ff93e6afb5e1de0d618edf5aa04fe31ea8aa766b`

上述报告的 JSON 格式校验通过。它们记录的是 2026-11-03 当时的仓库快照；仓库后续更新时应按记录的 `base_url` 和命令重新查询。此验证只说明对应 metadata 快照中的精确 Provides 和 RPM EVR 诊断结果，不证明 Go API 兼容性或完整依赖闭包。
