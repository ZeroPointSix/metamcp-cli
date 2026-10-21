# 发布 mcli

一次发布 = 推一个 `v*` tag，或在 GitHub 的 Actions → Release → Run workflow 里填入版本号（如 `v0.2.0`），由 Actions 自己创建 tag。`release.yml` 会：

1. **GitHub Release（主渠道）**：GoReleaser 交叉编译 6 个目标（linux / darwin / windows × amd64 / arm64），上传 `mcli_<os>_<arch>.tar.gz|zip` 和 `checksums.txt`。文件名不带版本号，`install.sh` / `install.ps1` 直接从 `releases/latest/download/` 取最新版。`-rc.N` 之类的 tag 自动标为 pre-release，不会被 latest 选中。
2. **npm（可选）**：仓库变量 `NPM_PUBLISH=true` 且配置了 `NPM_TOKEN` 时，再发布 `@zeropointsix/mcli` 及 6 个平台包；预发布进 `next`，正式版进 `latest`，已存在的版本自动跳过。不配置则跳过这一步。

## 一次性准备

| 事项 | 怎么做 |
| --- | --- |
| 仓库可见性 | 安装脚本、Releases 下载和 `go install` 都要求仓库公开。 |
| 许可证 | `npm/mcli/package.json` 目前是 `UNLICENSED`。公开发布前选定许可证（例如与 MetaMCP 一致），并在仓库根目录加 `LICENSE`。 |
| 包名 / scope | `@zeropointsix/mcli` 目前在 npm 上未被占用（不带 scope 的 `mcli` 已被他人占用）。需要 npm 上存在 `zeropointsix` 用户或组织并有发布权限。要换名，同时改 `npm/mcli/package.json`、`npm/mcli/bin/mcli.js`、`npm/build.mjs` 三处。 |
| npm 令牌（可选） | 在 npm 创建 **Granular Access Token**：只授予 `@zeropointsix` 下这 7 个包的 read/write，设过期时间；存为仓库 secret `NPM_TOKEN`。然后设仓库变量 `NPM_PUBLISH=true`。 |
| 真实端点冒烟（可选） | 仓库 secrets `MCLI_LIVE_URL`、`MCLI_LIVE_API_KEY`（建议用专门的只读测试 Key），之后可在 Actions 手动跑 `Live smoke test`。 |

## 每次发布

```bash
# 1. main 上 CI 全绿；对真实端点跑一次冒烟（本地或 Actions 手动触发）
MCLI_LIVE_URL=... MCLI_LIVE_API_KEY=... go test -run TestLive -v ./test/e2e/

# 2. 先发预发布，进 npm 的 next 通道
git tag v0.1.0-rc.1 && git push origin v0.1.0-rc.1

# 3. 在干净环境验证（不同平台各试一次）
curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | MCLI_VERSION=v0.1.0-rc.1 sh
mcli --version && MCLI_URL=... MCLI_API_KEY=... mcli search github --limit 1

# 4. 没问题再打正式 tag，进 latest
git tag v0.1.0 && git push origin v0.1.0
```

版本号遵循 semver。1.0 之前，破坏性变化（stdout JSON 结构、错误码、退出码、命令/flag）只升 minor 并写进 Release notes；1.0 之后这些都属于公开契约，变化需升 major。daemon 会按构建标识自动替换，用户升级后无需手动停旧进程。

## 出问题时

- **不要 unpublish**（npm 72 小时后不可撤，且会破坏已锁定的安装）。改用：
  - `npm dist-tag add @zeropointsix/mcli@<上一个好版本> latest` 把 `latest` 指回去；
  - `npm deprecate @zeropointsix/mcli@<坏版本> "原因，请升级到 x.y.z"`；
  - 修复后发新的 patch 版本。
- GitHub Release 可以删除或改为 draft；tag 一旦被 npm 用过就不要复用同一版本号。

## 给 Agent 的使用方式

见 [AGENT.md](AGENT.md)。
