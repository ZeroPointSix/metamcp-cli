# mcli — MetaMCP CLI

让 Agent 在「能装 npm 包、但配不了 MCP」的环境里，通过终端使用 MetaMCP：一个 Go 单二进制，核心命令是 `config / search / execute`，另有 `tools / schema`（读目录、读 Schema 的快捷方式）和 `doctor`（端点自检）。工具发现、权限、审计、Hook、Code Mode 执行全部仍在 MetaMCP 服务端完成，mcli 只是一个普通的 MCP Streamable HTTP 客户端。

```text
Agent / Shell
  -> mcli config | search | execute
  -> 本地 daemon（自动拉起，复用一个 MCP Session）   ← 不可用时回退为进程内一次性模式
  -> MetaMCP /metamcp/<endpoint>/mcp
  -> tools/call: search_tools / execute（Facade + Code Mode）
  -> 上游 MCP Tools
```

设计依据：内部设计文档《mcli 技术方案 v0.1》（2026-10-20）；服务端行为按 MetaMCP 后端源码（1.0.81）核对。

## 安装

**Linux / macOS**（以及 Windows 的 Git Bash）：

```bash
curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh
```

**Windows PowerShell**：

```powershell
irm https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.ps1 | iex
```

脚本会识别系统和架构（amd64 / arm64），从 [GitHub Releases](https://github.com/ZeroPointSix/metamcp-cli/releases) 下载对应压缩包，按 `checksums.txt` 校验 SHA-256 后安装，不需要 root / 管理员权限：

- Linux / macOS 装到 `~/.local/bin/mcli`；Windows 装到 `%LOCALAPPDATA%\Programs\mcli\mcli.exe` 并加入用户 PATH。
- 指定版本：`MCLI_VERSION=v0.1.0`；指定目录：`MCLI_INSTALL_DIR=...`（PowerShell 先 `$env:MCLI_VERSION='v0.1.0'`）。
- 重复执行即升级；正在运行的 daemon 不受影响，下次调用时自动换成新版本。
- 检查是否有新版本：`mcli version --check`（`update_available`、升级命令）；`mcli doctor` 也会顺带报告。

也可以手动从 Releases 下载解压，或 `go install github.com/ZeroPointSix/metamcp-cli/cmd/mcli@latest`（Go 1.25+）。

仓库里还保留了 npm 分发（启动器 + 按平台的二进制包，`--ignore-scripts` 可用），配置 npm 令牌后即可启用，见 [docs/RELEASING.md](docs/RELEASING.md)。

## 配置

```bash
mcli config set --url https://host/metamcp/<endpoint>/mcp
printf '%s' "$KEY" | mcli config set --api-key-stdin   # Key 只从 stdin 读，不进 shell 历史和进程列表
mcli config show                                       # Key 只显示末 4 位
```

- 优先级：`--url` flag > 环境变量 `MCLI_URL` / `MCLI_API_KEY` > 配置文件。
- 配置文件：`os.UserConfigDir()/mcli/config.json`（目录 0700、文件 0600，临时文件 + rename 原子写入）；`MCLI_CONFIG` 可覆盖路径。
- URL 与 Key 都齐备时，`config set` 会做一次连通校验（initialize → `tools/list`，必须包含 `search_tools` 和 `execute`），失败则不保存；`--no-verify` 跳过。
- 鉴权头使用 `X-API-Key`（不用 `Authorization: Bearer`：端点同时开启 API Key 与 OAuth 时，Bearer 会先按 OAuth 校验）。

## 使用

```bash
mcli search github pull request            # 多个词自动拼成一个查询
mcli search "send email" --limit 10 --category gmail --mode keyword
mcli execute task.ts --timeout 2m --note "整理本周 PR"
mcli execute - <<'EOF'
const me = await tools.github.get_me({});
const list = await tools.list({ category: "github" });
return { login: me.login, githubTools: list.length };
EOF
```

`search` 原样透传 `{query, category, mode, limit, cursor, note}` 给 Facade `search_tools`；`execute` 只发送 `{code, timeout_ms, note}`，不暴露 `async` / `action`。长时间运行的上游操作由工具自身参数（如 `VPS__exec` 的 `expire_time_ms` / `kill_time_ms`）和 `--timeout` 控制。

### 浏览目录、读 Schema

`search_tools` 在服务端做检索排序，一次约 15–20 s；只想看「有哪些工具」或「某个工具的参数」时用下面两个命令，它们在沙箱里读缓存目录（`tools.list`），几秒返回：

```bash
mcli tools                              # 每个 server 及其工具名
mcli tools --category VPS               # 该 server 的工具 + 一行摘要
mcli tools --category github --declarations   # 再加 TypeScript 声明（需服务端支持）
mcli schema VPS__exec                   # 完整 inputSchema / outputSchema（+ 声明）
mcli schema VPS.run-script github__get_me     # 可一次多个；也接受 server.tool 写法
```

- 名字是逻辑名 `<server>__<tool>`，**原样复制**；很多名字带连字符，脚本里要写成 `tools.VPS["run-script"](args)`。
- `tools.VPS` 是惰性 Proxy，`Object.keys(tools.VPS)` 永远是空数组；枚举请用 `mcli tools --category VPS` 或 `tools.list({ category: "VPS" })`。
- 分类名不存在时（如 `vps`）返回 `E_TOOL_NOT_FOUND`，`hint` 列出端点上实际有的 server，`suggestions` 给出大小写/`_`/`-` 意义上最接近的那个。

### 工具名纠错

Agent 常把 `_` 和 `-` 混用（`VPS__run_script` → 实际是 `VPS__run-script`）。`execute` / `schema` / `doctor --probe` 遇到 `E_TOOL_NOT_FOUND` 时，CLI 会把名字里的 `_` 和 `-` 互换，查一次目录（只在这条失败路径上多一次 `tools.list`），把真实存在的名字写进 `error.suggestions`，`hint` 给出可直接复制的调用写法：

```jsonc
{"ok": false, "error": {"code": "E_TOOL_NOT_FOUND", "suggestions": ["VPS__run-script"],
  "hint": "Did you mean \"VPS__run-script\"? Call it as tools.VPS[\"run-script\"](args) or tools.call(\"VPS__run-script\", args).", ...}}
```

### 端点自检：`mcli doctor`

```bash
mcli doctor                                         # 连通、鉴权、服务端版本、server 列表与工具数
mcli doctor --expect github,VPS                     # 缺任何一个 server 就失败
mcli doctor --expect github,VPS \
  --probe github__get_me --probe VPS__list-profiles   # 对只读工具各调一次
mcli doctor --probe 'VPS__exec={"command":"true"}'   # 需要参数时在 = 后给 JSON
```

doctor 总是自己开一个 MCP Session（不经过 daemon），报告：

- `server`：`initialize` 返回的 `serverInfo.name/version` 与协商的协议版本；`latency_ms`；
- `servers`：端点上每个 server（category）及工具数，`tool_count`；
- `capabilities`：沙箱是否支持 `tools.search()`（`tools_search`）和 `tools.list({ declaration })`（`list_declaration`）——两者随 ZeroPointSix/metamcp-chatgpt#143 一起出现；
- `expect.missing`：`--expect` 里缺的 server（大小写不对会提示 did you mean）；
- `probes`：每个探测 `ok` / `auth_required`（上游需要在 MetaMCP 里重新授权，例如 `AUTH_REAUTHORIZATION_REQUIRED`，会带 `authorization_url`）/ `not_found`（带纠错建议）/ `timeout` / `error`；探测的返回值不会输出；
- `cli`：当前版本、最新 Release、`update_available`（`MCLI_NO_UPDATE_CHECK=1` 跳过）。

有缺失 server 或探测失败时以 `E_DOCTOR`（退出码 1）失败，完整报告仍在 `data`；连不上、Key 被拒等仍是原来的错误码（2/3/4）。**`--probe` 会真的调用工具，只能填只读工具。**

### 发布与重启

- **发布后冒烟**：`/health` 和容器 healthy 只说明进程起来了。把 `mcli doctor --expect <关键 server> --probe <只读工具>` 加进服务端发布脚本，在 `/health` 之后跑；非 0 退出就走脚本里已有的回滚：

  ```bash
  MCLI_NO_DAEMON=1 MCLI_NO_UPDATE_CHECK=1 mcli doctor --expect github,VPS,railway \
    --probe github__get_me --probe VPS__list-profiles || rollback
  ```

- **服务端重启期间**：重启会让所有 MCP Session 失效。
  - Session 被服务端忘掉（HTTP 404）的请求在执行前就被拒绝，CLI/daemon 自动重建 Session 并重试一次，调用方无感。
  - `initialize` 阶段的 502/503 表示还没有任何工具运行；仍是退出码 4，但 `hint` 会说明「重试是安全的」。
  - 已经送达、执行中途连接断开的 `execute` 返回 `E_NETWORK` / `E_SERVER` / `E_DAEMON`（退出码 4）：**结果不确定，先查目标状态**，不要盲目重跑写操作。
- **能力随版本变化**：`mcli doctor` 的 `capabilities` 显示当前服务端支持的沙箱写法；脚本因为服务端过旧而用到 `tools.search` / `tools.list({ declaration })` 失败时，`hint` 会改为推荐 `mcli search` / `mcli tools` / `mcli schema`。

### 脚本契约

- 脚本是**函数体**：可以直接用顶层 `await`，用 `return` 返回结果。
- 只能通过全局 `tools` 访问工具：`tools.<server>.<tool>(args)`、`tools.call("server__tool", args)`、`tools.list({ names, schema: true })`（读完整入参 Schema）、`tools.list({ category })`。
- 较新的 MetaMCP 还在沙箱里提供 `tools.search(query)` 和 `tools.list({ declaration: true })`；较早的版本会拒绝，发现工具时先用 `mcli search` 最稳妥。
- 不支持 `import` / `export`、npm 依赖、文件系统、网络、定时器；`console.*` 的输出会出现在结果的 `console` 字段。
- CLI 本地处理：去 BOM → 包成 `(async () => {\n<源码>\n})` → esbuild `Transform`（`.ts` 用 TS loader，`.js` 用 JS loader，`-`/未知后缀按 TS）擦除类型 → 提交 `return await (<转译结果>)();`。
- 本地预检（不发请求，退出码 2）：import/export（给出正确写法提示）、语法错误（行号已去掉包装偏移）、源码 > 256 KiB、转译后 > 64 KiB。
- 服务端报错的 `location.line/column` 相对于提交的代码；CLI 用 esbuild sourcemap 换算回你的源文件行列，并在 `error.message` 里附上 `file:line: 源码`。
- 注意 esbuild 会去掉注释，脚本里依赖注释的内容不会到达服务端。

## 输出与退出码

stdout **永远只有一个 JSON 对象**；stderr 只放给人看的提示。

```jsonc
// search 成功
{"ok": true, "data": {"tools": [...], "count": 1, "total": 29, "next_cursor": "Mg=="}}
// execute 成功
{"ok": true, "data": <return 的值>, "console": "...", "meta": {"trace_id": "...", "partial": false, "child_successes": 3, "child_failures": 0}}
// 失败
{"ok": false, "error": {"code": "E_RUNTIME", "message": "...", "trace_id": "...", "line": 4, "column": 7, "hint": "...", "exit": 1},
 "diagnostics": [...], "trace": [...], "meta": {...}}
```

`doctor` 和 `schema` 失败时也会带上 `data`（完整报告 / 已找到的工具）；`E_TOOL_NOT_FOUND` 可能带 `error.suggestions`。

| 退出码 | 含义 | 典型错误码 |
| --- | --- | --- |
| 0 | 成功 | — |
| 1 | 脚本或工具执行出错 | `E_RUNTIME`、`E_TOOL_NOT_FOUND`、`E_INVALID_ARGUMENT`、`E_FACADE`、`E_PROTOCOL`（含 `partial=true`）、`E_DOCTOR`（自检发现问题） |
| 2 | 用法 / 配置 / 本地预检失败 | `E_USAGE`、`E_CONFIG`、`E_SCRIPT_SYNTAX`、`E_SCRIPT_MODULE`、`E_SCRIPT_TOO_LARGE`、`E_NOT_METAMCP` |
| 3 | 认证失败 | `E_AUTH`（HTTP 401/403） |
| 4 | 超时或网络，**结果不确定** | `E_TIMEOUT`、`E_CHILD_TIMEOUT`、`E_CANCELLED`、`E_NETWORK`、`E_SERVER`（5xx）、`E_DAEMON` |
| 5 | 触及服务端限制 | `E_CALL_LIMIT`、`E_OUTPUT_LIMIT`、`E_MEMORY_LIMIT`、`E_TOOL_LIST_TOO_LARGE`、`E_RATE_LIMITED`（429）、`E_CAPACITY`（Code Mode 池满） |

退出码 4 的 `hint` 会明确告诉 Agent：先检查目标状态再决定是否重试，不要盲目重跑写操作。

注意：MetaMCP 的 Facade 结果是 host-safe 的（`isError` 恒为 false），所以 mcli 按负载识别失败：Code Mode 诊断信封（`ok/diagnostics/trace_id/stats`）、`{"error": ...}` Facade 错误，以及 `_meta["metamcp/codeMode"]`。

## Daemon

默认路径：CLI 自动拉起本地 daemon，daemon 持有一个长期 MCP Session，连续的 search/execute 不再触发服务端 `createServer` 和上游重新绑定；Agent 杀掉的是 CLI 进程，Session 在 daemon 手里，空闲退出时正常发 DELETE。

- **隔离**：daemon id = `sha256(url + "\n" + api_key)[:12]`，换 URL 或 Key 自然对应新 daemon，旧的空闲退出。Key 通过 stdin 管道交给 daemon，不进命令行参数、不进环境变量、不写日志。
- **IPC**：Unix Socket（`$MCLI_RUNTIME_DIR` > `$XDG_RUNTIME_DIR/mcli` > `/tmp/mcli-<uid>`，目录 0700 且校验属主；目录太深、socket 路径超长时自动改放 `/tmp/mcli-<uid>`；socket 0600，Linux `SO_PEERCRED` / macOS `LOCAL_PEERCRED` 校验对端 UID）；Windows Named Pipe（DACL 仅当前用户 SID）。绝不监听 TCP。每个请求一条连接、一行 NDJSON 请求 + 一行响应，带协议版本号。
- **单实例**：`<id>.lock` 文件锁；多个 CLI 同时冷启动只有一个拉起 daemon，其余等待就绪（最多约 1.5 s）。
- **版本替换**：每次先 `hello`，协议版本或构建标识（版本 + 二进制大小/mtime 指纹）不一致就让旧 daemon 退出再拉起新的。
- **Session 失效**：远端 404 Session not found（在执行前被拒绝）→ 重建 Session 并重试一次，并发请求只触发一次重建；超时、断连、5xx 一律不自动重试 execute。
- **取消传播**：CLI 被杀 → IPC 连接断开 → daemon 取消该请求 → go-sdk 向服务端发 `notifications/cancelled`。
- **并发**：同一 Session 上最多 8 个在途请求。
- **空闲退出**：默认 10 分钟无请求（`MCLI_DAEMON_IDLE=30m` 可调）→ DELETE Session → 删除 socket → 退出。日志在 `<runtime>/<id>.log`（>1 MB 轮转一次）。
- **回退**：`--no-daemon` / `MCLI_NO_DAEMON=1`，或 daemon 无法就绪时，进程内一次性执行（initialize → tools/call → DELETE，Ctrl-C/SIGTERM 时也会发 DELETE）。只有请求**未送达** daemon 时才回退；已送达但连接中断的 execute 返回 `E_DAEMON`（退出码 4），避免重复写入。
- 排障：`mcli daemon status`（pid、build、在途/已处理请求数、空闲剩余、Session 协议版本、服务端名称与版本、重建次数）、`mcli daemon stop`。

## 已核实的服务端事实（M0）

| 项目 | 结论 | 依据 |
| --- | --- | --- |
| 协议版本 | **固定 `2025-11-25`**。go-sdk v1.8 默认 `2026-07-28`，会先发不带 Session 的 `server/discover`；MetaMCP 对任何不带 `Mcp-Session-Id` 的 POST 都新建 Server 实例并登记 Session，只在 DELETE 时释放 → 每次握手会泄漏一个服务端 Session。服务端 TS SDK 1.29.0 最高支持 `2025-11-25`。 | `streamable-http.ts` POST 无 session 分支；`@modelcontextprotocol/sdk@1.29.0` `SUPPORTED_PROTOCOL_VERSIONS`；测试 `TestSessionReuseAndDelete` 断言 0 个孤儿 POST |
| 结果形态 | 成功：首个 text 块为返回值 JSON，`<console_output>` 另起一块；失败：完整诊断信封且 `isError=false` | 对生产端点实测 + `execute-code.ts` / `host-safe-result.ts` |
| 报错行号 | `diagnostics[].location.line/column` 为 1 起、相对提交代码（服务端已扣除自身包装）；`stack` 中行号多 +2 | 生产实测：3 行脚本第 3 行 throw → `location.line=3`，stack `code-mode-user.js:5:7` |
| 池满 | 返回 `E_RUNTIME` + "pool is at capacity"，mcli 归类为 `E_CAPACITY`（退出码 5） | `code-mode-runtime.ts` |

## 生产端点实测（2026-10-20）

全部为只读操作（检索、读目录、`github.get_me`、纯计算）。可用 `TestLive` 复跑（见「开发」）。

| 项目 | 结果 |
| --- | --- |
| 连通校验 | ✅ 错误 Key → 退出码 3；正确 Key → 校验通过，工具含 `search_tools` / `execute`；协商协议 `2025-11-25`。一次 initialize + tools/list + DELETE 约 6–9 s |
| search | ✅ 返回真实工具卡片；**服务端检索本身 15–20 s**（Session 复用后依旧，keyword 模式 ~15 s） |
| execute | ✅ TS 类型擦除、`console` 回传、`tools.list({ schema: true })`、`tools.github.get_me` 子调用；空脚本约 3.7 s |
| 行号回映 | ✅ 抛错位置准确映射回源文件行列 |
| 服务端超时 | ✅ `--timeout 1s` → `E_TIMEOUT`，退出码 4，附"结果不确定"提示 |
| 单 Session 并发 | ✅ 8 个 execute 并发打同一 Session 全部成功，服务端排队 ≤ 607 ms，无 429、无重建 |
| CLI 被杀 | ✅ `kill -9` 后 daemon 1.99 s 内记录 `E_CANCELLED`，Session 继续可用 |
| daemon 退出 | ✅ `daemon stop` → DELETE 0.66 s 完成，socket 删除 |

**M4：一次性 vs daemon（各 20 次 `execute`）**

| 模式 | 总耗时 | p50 | p95 | 最小 / 最大 |
| --- | --- | --- | --- | --- |
| 一次性 `--no-daemon` | 166.5 s | 8.08 s | 9.87 s | 7.29 / 9.94 s |
| daemon | **78.8 s** | **3.80 s** | **4.41 s** | 3.65 / 4.98 s |

daemon 快 2.1×，每条命令省约 4.3 s（服务端建 Server 实例 + 握手 + DELETE 的成本）；daemon 常驻 RSS 约 16 MB。

仍待处理（服务端侧，不在 CLI 范围）：

- [ ] 生产 `SESSION_LIFETIME`：尚未确认是否配置。**建议设为 30 分钟左右**：daemon 随沙箱被直接销毁时无法发 DELETE，只能靠服务端 TTL 回收；mcli 收到 404 会自动重建，所以 TTL 可以放心设短。
- [ ] search 15–20 s、空 execute 3.7 s 的服务端耗时：生产服务机有内存紧张的记录，值得单独排查。
- [ ] 限流按 Key 还是按 IP（8 路并发未触发 429）。
- [ ] Windows：CI 三平台全绿，但 Named Pipe 版 daemon 尚未在 Windows 真机交互验证。

## 开发

```bash
go test -race ./...                 # 单元 + fakeserver 集成 + 真实二进制 e2e（含 daemon）
MCLI_BENCH=1 go test -run TestLatencyComparison -v ./test/e2e/
MCLI_BENCH=1 MCLI_BENCH_URL=https://host/metamcp/<ep>/mcp MCLI_BENCH_API_KEY=... \
  go test -run TestLatencyComparison -v ./test/e2e/   # 对真实端点对比两种模式
MCLI_LIVE_URL=https://host/metamcp/<ep>/mcp MCLI_LIVE_API_KEY=... \
  go test -run TestLive -v ./test/e2e/                # 真实端点只读冒烟（11 项）
node npm/build.mjs 0.1.0            # 交叉编译 6 个平台并生成 npm 包到 npm/dist
```

`test/fakeserver` 用 go-sdk 实现了一个假 MetaMCP（`search_tools` / `execute` 的真实返回形态、host-safe 错误、401/404/429/5xx 注入、Session 过期、取消观测、Session 计数）。e2e 覆盖：Session 复用、空闲退出发 DELETE、一次性模式每次 DELETE、Session 过期透明重建、并发冷启动只拉起一个 daemon、CLI 被杀时服务端收到取消、新构建替换旧 daemon、行号回映、各退出码、Key 不出现在输出中。

本地基线（linux/amd64，`-trimpath -ldflags="-s -w"`）：二进制 12.2 MB（gzip 4.9 MB），`--help` 冷启动约 7.6 ms。

GitHub 上也可以手动触发 `Live smoke test` 工作流（需配置仓库 secrets `MCLI_LIVE_URL` / `MCLI_LIVE_API_KEY`）。

## 给其他 Agent 使用

接入流程（每个 Agent 一个 Key、凭证注入、验收）和可直接复制的提示词见 [docs/AGENT.md](docs/AGENT.md)。

## 发布

推 `v*` tag 即发布：GoReleaser 出 GitHub Release，`NPM_PUBLISH=true` 时同步发布 npm 平台包与启动器（`-rc.N` 进 `next`，正式版进 `latest`，可安全重跑）。一次性准备（仓库可见性、许可证、npm 令牌）和每次发布的步骤见 [docs/RELEASING.md](docs/RELEASING.md)。

## 范围外（MVP 不做）

浏览器 OAuth / 登录登出、异步任务（`execute async=true`）、多文件或带 import 的脚本、把上游工具映射成 CLI 子命令、人类友好的表格输出。后续候选：把类型擦除移到服务端 Code Mode，届时 CLI 可去掉 esbuild（约 4 MB）。
