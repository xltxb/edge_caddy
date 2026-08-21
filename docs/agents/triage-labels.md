# Triage Labels

技能内部用五个固定的 triage 角色说话。下表把它们映射到本仓库 issue tracker 里
**实际存在**的标签字符串。

| 技能里的角色 | 本仓库标签 | 含义 |
| --- | --- | --- |
| `needs-triage` | `待评估` | 维护者需要评估这个 issue |
| `needs-info` | `待补充信息` | 等提交者补充更多信息 |
| `ready-for-agent` | `可交给agent` | 已完整描述，AFK agent 可直接拿走 |
| `ready-for-human` | `待人工实现` | 需要人来实现 |
| `wontfix` | `不做` | 不会被处理 |

技能提到某个角色时（例如「打上 AFK-ready 的 triage 标签」），使用上表右列的字符串。

## 标签含中文，命令行里必须加引号

```bash
gh issue edit 3 --add-label "可交给agent"
```

不加引号时 shell 不会报错，只是把标签名截断或拆成多个参数，结果是**打上了一个
名字不对的标签**或凭空创建一个新标签——两种都不会失败，只会静默偏离。

## 仓库里那个孤儿 `wontfix`

`xltxb/edge_caddy` 里原本就有 GitHub 默认的英文 `wontfix` 标签。本映射用的是中文
`不做`，所以那个英文标签不再被任何技能使用。它不会造成错误，但会让标签列表里出现
两个语义相同的项。想清掉的话自己删（`gh label delete wontfix`）——
删仓库里已有的标签不该由工具自作主张。
