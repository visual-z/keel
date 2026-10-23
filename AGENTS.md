# AGENTS.md

{{PROJECT_NAME}}：⟨一句话：这是什么、为谁、最硬的约束是什么⟩。

本文件是 agent 指引的**唯一入口**，是地图不是百科（上限见 `keel.toml` 的 `docs.agents_max_lines`）。
常驻规则写这里；专题规则写 `docs/PLAYBOOK.md`；能机械检查的规则写成检查，不写成散文。
`CLAUDE.md` 只 import 本文件，不另写规则。设计依据见 `docs/PRINCIPLES.md`。

## 会话开始

1. 读 `docs/PROGRESS.md` 最上面一条，`git log --oneline -15`，`python3 tools/keel.py status`。
2. 动手前跑一次 `python3 tools/keel.py gate`，确认起点是绿的；起点就红，先报告，不要在红的基线上叠改动。
3. 找到当前的卡；没有卡就先开卡（见下文「任务卡」）。

## 命令

项目自己的构建与测试命令只写在 `keel.toml` 的 `[[gate.step]]` 里，下面的命令都从那里读。

- 全部检查：`python3 tools/keel.py gate`（第一条失败即停；`--record <卡>` 把回执写进卡）
- 开卡：`python3 tools/keel.py new <type> <slug> [--light]`
- 验证 RED：`python3 tools/keel.py verify-red docs/tasks/<卡>.md`
- 防弱化测试：`python3 tools/keel.py assert-guard`
- 完成定义冻结：`python3 tools/keel.py freeze-check`
- 只变异改动过的代码：`python3 tools/keel.py mutate`
- 完整手册：`docs/KEEL.md`

**完成** = `gate` 全绿 + 本卡 `verify-red` 通过 + `assert-guard` 通过 + 卡里 author 类检查由作者确认。

## 边界

### Never（违反即返工）

- 为了让任务通过而放宽完成定义、删除或弱化断言、加 skip、改阈值；达不到就停下报告（`assert-guard` / `freeze-check` 机械检查）
- 把模型的陈述当证据；「看起来对」「日志里有 passed」都不算，只认退出码与计数
- 手改生成物或快照；只用生成器重生成，然后读 diff
- 无界资源：没有上限的队列、缓冲、缓存、重试；没有超时的等待。上限常量集中定义并带 why
- 把秘密（token、密码、私钥、内网地址）写进代码、日志、错误信息、测试夹具或文档
- 把原始验证日志、完整命令输出、截图提交进仓库（卡里只记命令、退出码、计数、断言原文）
- `git add -A` / `git add .`；push、force push、amend 非本会话创建的提交
- 拷贝许可证不兼容的代码；引用宽松许可证的代码时登记出处

### Ask first（先与作者讨论）

- 推翻 `PLAN.md` 里的已定决策；改目标、非目标或里程碑的退出条件
- 新增依赖；改外部契约（协议、API、存储格式、配置格式）的语义
- 改预算、阈值、上限的数值
- 任何破坏性或不可逆操作；推送、发布、改远端

### Always

- **一次一卡，卡先于代码。** 开卡提交（卡 + 新的红测试）就是冻结点；之后完成定义只能加严，改动要在卡的 `## 修订` 里写原因并由作者确认。
- **先红后绿。** 行为改动必须有 `RED: <commit> <测试过滤串>`，或者在卡里放一段 `keel-inject` 故障补丁，并由 `verify-red` 复验。编译错误、加载错误、匹配 0 个测试都不算红。
- **退出码直接读，不经管道。** 管道里用 `set -o pipefail`；测试计数必须等于预期，只能因本卡的新测试而增加。
- **检查器也是代码。** 新增或修改任何检查（keel 规则、边界规则、CI 步骤），先故意违规一次，确认它真的会红。
- **不变量写下来。** 新不变量登记到 `docs/INVARIANTS.md`，配一个 property 测试或代码断言（测试构建里常开），不能只靠样例测试。
- **假设要对拍。** 对外部系统（OS、内核、第三方 API、模型供应商）的假设写进卡的「环境假设」，动工前先用真实环境对拍一次。
- **作者可见的改动停在 `awaiting-author`。** 钉几何或调用过某个 API 的测试，不能证明「看得到、点得到、拖得动」。
- **中途决策先找证据。** 先查高 star 项目、竞品与社区讨论的既有做法，带证据提出；不自行推翻已定决策。
- **同一错误第二次出现：** 能机械化就写成检查；不能的，在 `docs/PLAYBOOK.md` 对应主题下补一条「一句话 + 判据」，不写事故叙述。
- **新术语先进 `CONTEXT.md`**，再用于代码、文案、协议字段。
- **提交只暂存显式路径**，提交前 `git status --short` 逐个核对；Conventional Commits，祈使句写 why。
- **会话收尾。** 仓库干净、可构建；`docs/PROGRESS.md` 最上面追加：做了什么 / 卡在哪 / 下一步。

## 任务卡

- 位置 `docs/tasks/<type>-<slug>.md`；type ∈ feat fix perf refactor debt docs chore research review release。
- 完整卡用 `TEMPLATE.md`；同时满足「不改外部契约、不加依赖、改动 ≤ 5 个源文件」的修复和清理可以用 `TEMPLATE-LIGHT.md`。做到一半超出条件就停下，改用完整卡。
- 状态：todo → doing → (blocked | awaiting-author) → done。状态由 `lint` 校验。
- 每条完成定义有稳定编号 `D<n>`，下面至少一行 `- 检：<kind> …`，kind 取 test / property / inject / cmd / grep / author / measure。
- 完整格式与示例：`docs/KEEL.md`、`docs/examples/fix-example.md`。

## 代码风格

- 注释写意图、数据流和为什么这样设计，不复述代码；非显然的约束要能追溯到 PLAN 里的决策或某张卡。
- 常量带 why（存在理由 + 失效后果），并注明数值是实测的还是假设的；假设值要附测量方法。
- 测试名是完整句子，一个测试证明一条契约；边界值必跑 0、1 和上限。
- 新逻辑先问能不能写成纯函数：能，就配 property 测试；不能，就让 I/O 层保持薄。
- 新增依赖要写为什么需要；刻意不引入的东西写为什么不引入。

## 架构速览

⟨依赖方向、分层、热路径的形状。能写成 `keel.toml` 里 `[[boundary.rule]]` 的边界就写成规则，这里只画图。⟩

## 深读地图

| 要做什么 | 先读 |
|---|---|
| 目标、非目标、决策、里程碑 | `PLAN.md` |
| 词表与命名禁区 | `CONTEXT.md` |
| 为什么是这套流程 | `docs/PRINCIPLES.md` |
| 专题操作规则 | `docs/PLAYBOOK.md` |
| 卡片格式与 keel 命令 | `docs/KEEL.md` |
| 已登记的不变量 / 可复用的验证夹具 | `docs/INVARIANTS.md` / `docs/FIXTURES.md` |
| 接续上一个会话 | `docs/PROGRESS.md` |
