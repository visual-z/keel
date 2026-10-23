---
type: fix
status: done
weight: full
created: 2026-01-05
baseline: 3c1f0a9e2b7d
---

# 分页游标在最后一页之后不再返回重复条目

<!-- 这是一张填好的示例卡，演示完整卡的每一节。它不在 docs/tasks/ 下，所以不参与 lint 和 freeze-check。 -->

## 来源

用户反馈「最后一页翻两次会看到重复数据」。在本地用 10 001 条记录、每页 100 条复现：第 101 页返回了第 100 页的最后一条。
这个缺陷一直就有，从分页功能引入起就存在，不是哪次改动带进来的。

## 范围

- **独占：** `src/pagination/`
- **不碰：** 游标的外部编码格式（客户端已缓存旧游标）；`MAX_PAGE_SIZE` 的取值；既有分页测试的断言。

## 不变量

- INV-3：对任意数据集和页长，逐页遍历拿到的条目恰好覆盖全集一次，不重不漏。检查方式：property（固定种子）+ 代码断言。

## 观测面

只看 API 返回的条目 id 序列和 `next_cursor` 是否为空，不看内部计数器。
GREEN 可能被截断的方式：property 测试在第一页就 `return`，或者生成器只产出空数据集。挡法：property 断言「遍历的总条数等于数据集大小」，并且生成器的最小规模为 1。

## 环境假设

- 数据库按 `(created_at, id)` 排序时，结果稳定，即使存在相同的 `created_at`。对拍：在真实数据库上插入 1 000 条 `created_at` 相同的记录，连续查询 20 次，顺序一致。结果：一致（对拍脚本在 `scripts/check-sort-stability.sh`，输出不入库）。

## 完成定义（动工前冻结；实现中不得放宽）

- **D1** 最后一页之后再请求，返回空列表，且 `next_cursor` 为空。
  - 检：test `pagination_after_last_page_returns_nothing`
- **D2** 任意数据集、任意页长，逐页遍历不重不漏。
  - 检：property `pagination_covers_every_item_exactly_once`
- **D3** 把边界比较 `>` 改回 `>=` 时，测试必须变红。
  - 检：inject `pagination_`
- **D4** 不弱化既有测试；测试计数 412 → 414。
  - 检：cmd `python3 tools/keel.py assert-guard`

## 设计

游标从「偏移量」改为「最后一条的 `(created_at, id)`」，翻页查询改为 `WHERE (created_at, id) > cursor`。
否决的方案：
- 在偏移量上 `+1` 修补：并发插入时仍会重复或遗漏。
- 每页多取一条来判断末页：只能修好 D1，修不了 D2。
外部游标仍然是不透明的 base64 字符串，旧游标按偏移量解码后转成键集游标，客户端不需要改动。

## 验证命令

```bash
python3 tools/keel.py verify-red docs/tasks/fix-pagination-duplicate.md
python3 tools/keel.py assert-guard
python3 tools/keel.py gate --record docs/tasks/fix-pagination-duplicate.md
```

## 证据

RED: 7d2e4f1a9c3b pagination_  # D1, D2

```diff keel-inject pagination_
--- a/src/pagination/cursor.py
+++ b/src/pagination/cursor.py
@@ -41,1 +41,1 @@
-    rows = query.where(key > cursor.key)
+    rows = query.where(key >= cursor.key)
```

失败断言原文（RED 时）：`AssertionError: id 9900 returned twice (pages 100 and 101)`

GATE: 5b8c2d7e1f0a+dirty ok build=0 test=0[414p/0f] boundaries=0 docs=0 cards=0 freeze=0 (2026-01-06)

## 修订

（无）
