# 第二阶段真实仓库查询记录

日期：2026-10-31

本记录对应第二阶段分支的本地验证，不是 openRuyi 官方发布声明。

## 查询命令

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --gpg-keyring /usr/share/pki/rpm-gpg/RPM-GPG-KEY-openruyi-obs \
  --require-signature \
  --require 'go(aead.dev/mem)>=0:0.2.0' \
  --require 'go(example.test/missing)>=0:1' \
  --format json
```

`riscv64` 查询只将 URL 和 `--arch` 替换为 `https://repo.build.openruyi.cn/openruyi/riscv64/` 与 `riscv64`。keyring 是由 openRuyi 仓库中的 `openruyi-repos` recipe 提供的维护者信任锚；工具不会下载远程 key。

## 结果摘要

| Repository | Arch | repomd revision | repomd SHA256 | Signature | `go(aead.dev/mem)` | `go(example.test/missing)` |
|---|---|---:|---|---|---|---|
| openruyi/x86_64 | x86_64 | 1793358268 | `de0a004c575e46074921dd3bcd29a4123c425d95649f91aa70791eb7506774c6` | verified | satisfied | missing |
| openruyi/riscv64 | riscv64 | 1793291218 | `1a9517041516a76ac35acb35deb6e02a3f50d0565180ab252c36d4115bd999a1` | verified | satisfied | missing |

> 两个快照的签名 fingerprint 都是 `51101B1408F35F75BE9C12807447DA4C8D2E5D3A`。查询中的 `>=0:0.2.0` 刻意省略 RPM Release，因为仓库 capability 提供的是带 `EQ` 的 `0.2.0`，没有提供 Release；比较器不会臆造 release。若用户要求一个 capability 未声明的 EVR 部分，结果会是 `unknown`，而不是错误地判定满足/不足。

x86_64 provider 示例：

```text
go(aead.dev/mem)
package: go-aead-dev-mem
arch: noarch
provide EVR: 0.2.0
location: noarch/go-aead-dev-mem-0.2.0-3.28.or.noarch.rpm
```

riscv64 provider 的 package release 与 x86_64 快照不同，因此报告分别保存目标架构和 metadata 身份。两次查询没有读取宿主 rpmdb；primary metadata 只保留目标架构与 `noarch` binary package 的精确 `go(...)` Provides。

## 可复现证据

本次 JSON 报告和 stderr 位于本地证据目录：

```text
/home/wsl/go2spec-phase2-evidence-20261031-final/x86_64.json
/home/wsl/go2spec-phase2-evidence-20261031-final/riscv64.json
/home/wsl/go2spec-phase2-evidence-20261031-final/x86_64.stderr
/home/wsl/go2spec-phase2-evidence-20261031-final/riscv64.stderr
```

报告文件 SHA256：`x86_64.json` = `aa82c766bd45d55828a3793e2b240769b910775c9b8ba58a98f357781ce0e7c3`；`riscv64.json` = `dbebe886ed84129bc2fd90d68002860fd0cc2cc064457d58f609862804d0d5ce`。
