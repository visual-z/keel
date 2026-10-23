# 设计原则

这套工作区把一张任务卡当作**一个待证明的命题**：完成定义是规约，测试是证明，`keel` 是证明检查器。
下面每条原则都写明它来自哪里、落到了哪个机制上。机制改了，先回来看原则还成不成立。

## 一、检查器只有一个，而且要小

**来源。** 形式化验证里，人们不信任写证明的一方，只信任证明检查器；检查器越小，可信基越小。
在 LLM 写证明的大规模实验里，模型写出的 Rocq 证明约十亿行，其中只有约 1.8 亿行最终通过检查器
（Anthropic，*LLMs are making formal software verification feasible at a much larger scale*，附录，2026-10）。
写出来不等于成立，只有检查器说了算。

**机制。**
- 可信内核是 `tools/keel.py`：单文件、只用标准库、技术栈相关的知识全放在 `keel.toml` 里。
- 完成只由退出码和计数证明，不由叙述证明。
- keel 自己也有测试，而且每项检查都要演示过「抓得住它要抓的违规」（`tools/tests/`）。

## 二、规约先于实现，而且要短

**来源。** 同一份附录里，agent 几乎总是先写一份**很短的规约**（中位数约 110 行），再证明两个实现都符合它；
被证明的程序中位数约 86 KB。规约短，人才审得过来，也才知道到底证明了什么。

**机制。**
- 完成定义在动工前提交，这次提交就是冻结点。
- 每条完成定义有稳定编号 `D<n>`，并绑定一个检查（`- 检：…`）。没有检查的完成定义只是愿望，`lint` 会拒绝。
- `freeze-check` 拿当前卡和冻结点做 diff：条目可以增加（加严），不能悄悄改或删；要改，就在 `## 修订` 里写原因，并由作者确认。

## 三、空洞的证明是最危险的证明

**来源。** 一条恒真的规约什么都能「证明」。测试也一样：从没红过的测试，证明不了它在测东西。

**机制。**
- `RED: <commit> <过滤串>`：`verify-red` 在临时 worktree 里检出那个 commit 重跑，必须真的红。匹配 0 个、编译错误、加载错误、超时、被信号杀死，都不算红。
- 修好之后，在当前工作树再跑一次，必须是绿的。
- RED 用的 commit 会钉在 `refs/keel/red/<sha>` 下，squash 或 reflog 过期都抹不掉这份证据。

## 四、测试会漏，所以要往实现里注错

**来源。** 附录里，研究者往解释器里故意注入 7 个 bug：随机测试漏掉了 5 个，证明一个不漏。
另有研究发现，agent 写的测试覆盖面很全，断言却很弱：把函数体换成 `return 0`，测试照样通过（SWE Atlas）。
Google 的做法是只变异代码评审里改到的行（Petrović 等，2021）。

**机制。**
- 卡里可以放 `keel-inject` 故障补丁。`verify-red` 把补丁打到 HEAD 上，要求测试变红；补丁只能改产品代码，不能改测试。这样「我故意改坏过一次」就成了机器可以复验的证据。
- `keel mutate` 只对本分支改过的代码跑变异测试（例如 `cargo mutants --in-diff`）。存活的变异体，就是测试分辨不出的改动。

## 五、写清楚「观测面」，再找它的截断

**来源。** 附录把「两个程序行为相同」定义为：stdin/stdout/stderr 的字节、退出码、最终文件系统都相同。
但程序崩溃或死循环时，轨迹只是**截断**，所以「一个崩溃、一个死循环」也会被判为等价。
为此他们补了第三条定理：未插桩的那个二进制也不崩溃。

**机制。** 完整卡必须写「观测面」：本卡靠什么判定行为，以及 GREEN 可能被怎样截断（提前 return、走了跳过的分支、异常被吞掉、断言之前就退出了），各用什么挡住。
`assert-guard` 会在 diff 里查删掉的断言、新增的 skip/only、测试里新增的 `exit` 和 `__eq__` 覆盖。
后两种正是已有文献记录的 reward hacking 手法：「断言前退出」和「永远相等」（Anthropic，*Natural emergent misalignment from reward hacking*）。

## 六、对着真实产物证明

**来源。** 附录直接证明编译后的 RISC-V 二进制，不把程序先翻译成 Rocq，也不去解释源码：
翻译可能出错，解释器也可能和真实二进制的行为不一致。

**机制。**
- `verify-red` 跑的是 git 里的真实 commit，不是拷出来的一段代码片段。
- 用户可见的改动，要在真实环境里（真实终端、真实浏览器、生产构建）实证一次，结论写进卡。
- 作者可见的交互停在 `awaiting-author`，由作者手测后确认。

## 七、规约要和现实对拍

**来源。** 附录里，Python 解释器的形式语义和 CPython 是否一致，是靠跑 170 万个（大多随机生成的）程序经验性地确认的。规约本身也可能错。

**机制。** 完整卡必须写「环境假设」：对 OS、内核、第三方 API、模型供应商的每条假设，都要写明怎么和真实系统对拍，而且对拍先于实现。
测试夹具的形状必须是真实系统会返回的形状，不能是让实现好过的形状。

## 八、没建模的行为一律算失败

**来源。** 附录把「崩溃」定义为故障、abort，**或任何未建模的行为**（例如语义里没定义的 syscall）。

**机制。** keel 遇到认不出的输出格式就报失败，不会当成通过；新路由、新权限在没有分类之前默认拒绝；
「未声明」记作 unknown，不当作 false。这条原则落进代码里，就写成 `assertNever` 一类的穷尽检查。

## 九、代码里的断言就是规约

**来源。** OSS-Fuzz 的入口点本身就带维护者写的 `assert`。abort 算崩溃，所以证明「不崩溃」，就顺带证明了这些断言永不触发；
LZO 的「压缩再解压，长度相等」这条断言，就逼着证明去覆盖整个 round-trip。
他们的 mini-ASan 只用约 100 条指令就能检查每一次内存访问（真实的 ASan 运行时要 10 万条以上）。

**机制。** `docs/INVARIANTS.md` 登记的不变量，优先写成代码断言，在测试构建里常开（如 Rust 的 `debug_assert!`）。
这样已有的每一个测试，都会在每一帧、每一次状态转移上替你检查它们。廉价的领域 sanitizer 比再写一百个样例测试更划算。

## 十、冗长会撞墙：把重复的推理抽成引理

**来源。** 附录里，391 行的 cJSON 入口配了 63 万行证明，其中只有约十分之一和 cJSON 本身有关：
libc、分配器、ASan 运行时，每个证明都要从头再证一遍。人类专家会抽出调用约定、不相交指针、分配器行为这类抽象；
作者判断冗长风格会撞上复杂度墙。软件工程上的对应现象是：AGENTS.md 越写越长，任务卡上千行，规则手册上百条。

**机制。**
- **验证夹具当引理。** 可复用的验证环境登记在 `docs/FIXTURES.md`，卡里按名字引用，不重新描述。
- **证据由脚本生成。** `gate --record` 写一行结构化回执，卡里只写本卡特有的东西。
- **规则有预算。** AGENTS.md 和 PLAYBOOK 都有上限（`check-docs` 强制）。超了就合并规则，或者把能检查的部分写成检查、删掉对应的散文。
- **AGENTS.md 是地图，不是百科。** 这是 OpenAI harness engineering 的经验：先试了「一个大 AGENTS.md」，失败了，改成约 100 行的目录，指向 `docs/` 下的权威文档。

## 十一、分层推进，每层都有自己的定理

**来源。** 附录把 Python 解释器分成 17 层，每层一个定理；比较不同模型时，把真实的 fuzz 入口缩到 283–2,491 条指令，分三组难度。

**机制。** `PLAN.md` 的里程碑按层写退出条件，每层的退出条件都能检查。
遇到大问题，先缩成最小复现，再开卡。

## 十二、上下文是稀缺资源

**来源。**
- ETH Zurich 的评测发现：agent 会遵守上下文文件里明确的指令，但仓库摘要对它没有帮助。
- 对 AGENTS.md 的通行建议是：只写 agent 从代码里推断不出来的东西；能 lint 的规则交给工具。
- Anthropic 的长时 agent harness：会话之间靠进度文件和 git 记录交接，每个会话开始先确认基线是好的，一次只推进一项。

**机制。** AGENTS.md 只写指令，不写介绍；`docs/PROGRESS.md` 每次三行，最新在最上；会话开始先跑 gate 确认基线。

## 参考

- Anthropic, *LLMs are making formal software verification feasible at a much larger scale*（附录，2026-10-22）
- Anthropic Engineering, [Effective harnesses for long-running agents](https://anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- OpenAI, [Harness engineering: leveraging Codex in an agent-first world](https://openai.com/index/harness-engineering/)
- GitHub, [spec-kit](https://github.com/github/spec-kit)：Specify → Plan → Tasks → Implement 的规约驱动流程
- [cargo-mutants: testing code changed in a diff](https://mutants.rs/in-diff.html)；Google 的 diff 内变异测试（Petrović 等，2021）
- [ImpossibleBench: measuring reward hacking in LLM coding agents](https://www.lesswrong.com/posts/qJYMbrabcQqCZ7iqm/impossiblebench-measuring-reward-hacking-in-llm-coding-1)
- [AGENTS.md as a testing contract](https://www.testmuai.com/blog/agents-md/)：完成定义写成退出条件，而不是形容词
