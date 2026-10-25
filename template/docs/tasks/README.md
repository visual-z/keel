# 任务卡

一张卡对应一个待证明的命题。格式和命令见 [docs/KEEL.md](../KEEL.md)，填好的示例见 [fix-example.md](../examples/fix-example.md)。

```bash
keel new fix ⟨slug⟩ --light   # 开卡
keel status                 # 看板
```

流程（两次提交，第一次就是冻结点）：

1. **开卡提交。** 提交卡（`status: doing`）和新写的红测试，例如 `test(scope): pin <契约>`。这个 commit 就是 RED commit，也是 `freeze-check` 的冻结点。pre-commit hook 认得这次提交：卡在本次提交里暂存，就算已冻结；离开 hook 时，只有真正的提交才算冻结。如果测试还不能提交，就用 `keel snapshot <测试文件>` 生成一个私有的 RED 快照。
2. **实现提交。** 修复代码，跑 `verify-red`、`gate --record`（它会从开卡提交起跑 assert-guard），然后把状态改成 `done`（作者可见的改动改成 `awaiting-author`），和实现一起提交。
