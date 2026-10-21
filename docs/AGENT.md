# 让 Agent 使用 mcli

## 接入流程

1. **每个 Agent 一个 API Key**：便于审计、限流和单独吊销；能只读就只读。
2. **安装**：Linux / macOS `curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh`；Windows `irm https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.ps1 | iex`。装到用户目录，确保它在 PATH 上（Linux / macOS 为 `~/.local/bin`）。
3. **注入凭证**：用平台的 secret 功能设置环境变量 `MCLI_URL`、`MCLI_API_KEY`。不要把 Key 写进提示词、仓库或命令行参数。
4. **验收**：在 Agent 环境里运行 `mcli search github --limit 1`，输出 `"ok": true` 即可。
5. **提示词**：把下面这段放进系统提示、`AGENTS.md` / `CLAUDE.md` 或一个 Skill。具体用法由 Agent 通过 `mcli --help` 自己探索。

每条命令都跑在用完即毁的新容器里时，设 `MCLI_NO_DAEMON=1`：否则后台 daemon 会随容器销毁，来不及释放服务端 Session。

## 提示词

```text
你可以用命令行工具 `mcli` 调用 MetaMCP 上聚合的工具。凭证已通过环境变量配置好，不要打印、回显或写入文件。

- 先运行 `mcli --help` 了解用法：`mcli search` 找工具，`mcli execute` 运行调用工具的 TypeScript 脚本。
- 参数不确定时先查清工具定义，不要猜。
- stdout 是一个 JSON 对象。按退出码处理：0 成功；1 根据错误修正后重试；2 按 hint 改写；3 认证失败，停止并报告；4 结果不确定，先确认目标状态再决定是否重试，绝不盲目重跑写操作；5 触及服务端限制，拆小或稍后再试。
- 有副作用的操作（创建、修改、发送、删除）先查出候选对象并确认，再单独执行写入。
- 用 `--note` 写一句调用目的，会记入审计日志。
```
