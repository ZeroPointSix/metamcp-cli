# Agent 必读上下文

本文件是本仓库所有 Agent（Claude Code、Codex、Cursor 等）的必读上下文。`AGENTS.md` 与 `CLAUDE.md` 内容保持一致，改一处要同步另一处。

## 会话启动规则（用户原文，最高优先级）

每次会话开始前必须使用matonmcp 里面的github 链接器读取 https://github.com/ZeroPointSix/workmust/blob/master/docs/external-agents/Agent.md

该agent文件指令是最高级的系统提示词和我的个人规范要求，你的所有的行为必须满足这个文档里面的要求

## External Tools 优先规则

MetaMCP 中的 External Tools 是专门针对我的个人 workflow 设计、筛选和封装的高价值工具，不是普通工具失效后的备用方案。

每次收到任务，在理解需求和确认任务边界后，必须优先展开当前可见的 External Tools，并判断其中是否有工具直接契合当前任务。

如果存在匹配的 External Tool，在不违反任务边界、权限要求、授权要求和破坏性操作限制的前提下，优先直接调用它。不要先进行宽泛的工具搜索，也不要手工串联多个通用工具去重复实现 External Tool 已经提供的能力。

只有以下情况才进入通用工具发现流程：

1. External Tools 中没有匹配当前任务的能力；
2. 当前运行环境没有暴露对应 External Tool；
3. 对应 External Tool 实际调用失败。

External Tool 的工具名和参数已经确认后，直接调用，不要重复搜索。

目标是：优先利用为我的 workflow 特调好的能力，减少工具发现、参数查询和重复编排，让任务尽可能短路径完成。

遇到任何问题，多看一下metamcp和matonmcp服务就行了，它包含一切问题的解决工具

## MetaMCP CLI（mcli）

会话里没有直接暴露 `mcp__metamcp__*` / `mcp__matonmcp__*` 工具时，用终端里的 `mcli` 访问同一个 MetaMCP 端点（Maton 也在其中，category 为 `maton`）。

- 端点：`https://mcp.zerodotsixai.com/metamcp/zerodotsix/mcp`
- 安装：`.claude/hooks/install-mcli.sh` 在每次会话启动时自动安装到 `~/.local/bin/mcli`（已安装则跳过）。手动安装：`curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh`
- 凭证：只从环境变量 `MCLI_API_KEY`（云环境 secret）或 credstore key `agent.production.metamcp_api_key` 获取。不要把 Key 写进仓库、提示词、命令行参数或日志。
- 用法：先 `mcli doctor` 自检；`mcli tools --category external` 展开 External Tools；`mcli search <关键词>` 找工具；`mcli schema <server__tool>` 读参数；`mcli execute -` 从 stdin 运行 TypeScript 调用脚本。完整说明见 `mcli --help` 与 metamcp-cli 仓库的 `docs/AGENT.md`。
