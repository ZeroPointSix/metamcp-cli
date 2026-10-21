# 让 Agent 使用 mcli

## 接入流程

1. **每个 Agent 一个 API Key**：便于审计、限流和单独吊销；能只读就只读。
2. **安装**：Linux / macOS `curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh`；Windows `irm https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.ps1 | iex`。装到用户目录，确保它在 PATH 上（Linux / macOS 为 `~/.local/bin`）。
3. **注入凭证**：用平台的 secret 功能设置环境变量 `MCLI_URL`、`MCLI_API_KEY`。不要把 Key 写进提示词、仓库或命令行参数。
4. **验收**：在 Agent 环境里运行 `mcli doctor --expect <这个 Agent 要用的 server>`（例如 `--expect github,VPS`），退出码 0 即可；缺 server 时会以 `E_DOCTOR` 失败并指出缺哪个。需要确认上游鉴权正常时再加 `--probe <只读工具>`（例如 `--probe github__get_me`），`auth_required` 表示要在 MetaMCP 里重新授权该上游。监控脚本同理：每次检查前先跑一次 doctor，避免「端点里根本没有这个 server」时连续报 unknown tool。
5. **提示词**：把下面这段放进系统提示、`AGENTS.md` / `CLAUDE.md` 或一个 Skill。具体用法由 Agent 通过 `mcli --help` 自己探索。

每条命令都跑在用完即毁的新容器里时，设 `MCLI_NO_DAEMON=1`：否则后台 daemon 会随容器销毁，来不及释放服务端 Session。

升级：`mcli version --check` 显示是否有新版本；重新执行安装脚本即升级。

服务端发布/重启期间：Session 全部失效。404 会被自动重建并重试；`initialize` 阶段的 502/503 表示什么都没运行，可以稍后重试；已送达的 `execute` 中途断开（`E_NETWORK` / `E_SERVER` / `E_DAEMON`，退出码 4）结果不确定——先查目标状态，不要盲目重跑写操作。

## 提示词

```text
你可以用命令行工具 `mcli` 调用 MetaMCP 上聚合的工具。凭证已通过环境变量配置好，不要打印、回显或写入文件。

- 先运行 `mcli --help` 了解用法：`mcli search` 找工具，`mcli execute` 运行调用工具的 TypeScript 脚本；`mcli tools --category <server>` 列出某个 server 的全部工具，`mcli schema <server__tool>` 读完整参数定义。
- 参数不确定时先用 `mcli schema` 查清工具定义，不要猜。
- 工具名从 search/tools 的输出原样复制。很多名字带连字符，脚本里写成 `tools.VPS["run-script"](args)`；报 `E_TOOL_NOT_FOUND` 时看 `error.suggestions`。`Object.keys(tools.VPS)` 是空的，不能用来枚举工具。
- stdout 是一个 JSON 对象。按退出码处理：0 成功；1 根据错误修正后重试；2 按 hint 改写；3 认证失败，停止并报告；4 结果不确定，先确认目标状态再决定是否重试，绝不盲目重跑写操作；5 触及服务端限制，拆小或稍后再试。
- 有副作用的操作（创建、修改、发送、删除）先查出候选对象并确认，再单独执行写入。
- 用 `--note` 写一句调用目的，会记入审计日志。
```
