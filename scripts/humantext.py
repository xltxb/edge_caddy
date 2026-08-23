#!/usr/bin/env python3
"""面向人的字符串里，有没有渲染不出来的标记？

契约 §0.3 说得很清楚：`msg` 与 `reason` 由后端产生、**前端原样显示**。
原样的意思是原样——写 `**域名**` 就会在界面上看到两对星号。

这个脚本存在的理由不是那一次，是**它的成因不会消失**：这个仓库的注释
大量用 `**…**` 做强调，而注释和它下面那行字符串是同一口气写出来的。
风格不会改，所以这个毛病会反复发生。

一次撞出来之后回头扫全量，找到 9 处，其中 3 处是面向用户的
（`capabilities.notes` 那条当时正显示在 DNS 页上）。
**修完一个 bug，同形状的另一个不会自己浮出来**——除非有东西替你扫。

判据：非注释行里的双引号字符串字面量，含 `**` / 反引号 / `__前缀`。

盲区（跟误报一样要说清）：
  - 反引号原始字符串（`...`）不看：正则、SQL、JSON 模板都住在那里，
    而它们不面向人。代价是一条用反引号写的 msg 会漏掉。
  - 拼接出来的标记看不见：`"**"+x+"**"` 每一段都不含 `**`。
  - 只看 Go。前端有自己那份（`pnpm check:humantext`），管辖各自的字符串。
  - 「面向人」是按「是不是字符串」判的，不是按「它到不到得了屏幕」判的。
    所以日志、终端报错也被算进来——那是有意的：终端同样不渲染 markdown。
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# 只挑 markdown 里**会被人当成标记**的那几个。句号、括号、书名号不算。
MARKS = [
    ("**", "粗体星号"),
    ("__", "粗体下划线"),
]


def go_files():
    out = []
    for d in ("internal", "cmd"):
        for p in (ROOT / d).rglob("*.go"):
            if p.name.endswith("_test.go"):
                continue  # 测试里的字符串不面向用户，且正则字面量会假阳
            out.append(p)
    return sorted(out)


def strings_in_file(text):
    """扫出整个文件里的双引号字符串字面量，返回 (行号, 内容)。

    **剥注释这一步比判据本身更要紧。** 这里是一个走完整个文件的状态机，
    而不是逐行做正则——逐行的两种偷懒各自会坏一个方向，两个我都亲手做过：

      1. `line.split("//")[0]`：字符串里的 `http://` 会被拦腰截断，
         `"见 http://x 里的 **说明**"` 就此**假阴**。
         （前端 agent 提前警告过这一格，我照样踩了。）
      2. 只跳过「整行以 `//` 或 `/*` 开头」：`/* … */` 块注释的**中间几行**
         会被当成代码扫，注释里的强调变成**假阳**。
         （他在自己那版里撞到的正是这个，8/11 是误报。）

    第 2 种尤其贵：**一个八成是误报的检查器，第二次就没人看了。**
    它不会被删掉，会被忽略——而忽略和不存在没有区别，只是还占着位置。
    """
    out = []
    i, n, line = 0, len(text), 1
    while i < n:
        ch = text[i]
        if ch == "\n":
            line += 1
            i += 1
        elif text.startswith("//", i):
            j = text.find("\n", i)
            i = n if j < 0 else j
        elif text.startswith("/*", i):
            j = text.find("*/", i + 2)
            seg = text[i:n if j < 0 else j + 2]
            line += seg.count("\n")
            i = n if j < 0 else j + 2
        elif ch == "`":  # 原始字符串：正则 / SQL / JSON 模板住在这里，不面向人
            j = text.find("`", i + 1)
            seg = text[i:n if j < 0 else j + 1]
            line += seg.count("\n")
            i = n if j < 0 else j + 1
        elif ch == "'":  # rune 字面量，'\'' 要认
            i += 1
            while i < n and text[i] != "'":
                i += 2 if text[i] == "\\" else 1
            i += 1
        elif ch == '"':
            start_line = line
            i += 1
            buf = []
            while i < n and text[i] != '"':
                if text[i] == "\\":
                    buf.append(text[i:i + 2])
                    i += 2
                else:
                    if text[i] == "\n":  # 不该发生（Go 不允许），但别把行号数丢了
                        line += 1
                    buf.append(text[i])
                    i += 1
            i += 1
            out.append((start_line, "".join(buf)))
        else:
            i += 1
    return out


def main():
    files = go_files()
    # **先证明自己不瞎。** 一次什么也没找到的扫描，要先证明它能找到什么。
    total_strings = 0
    bad = []
    for p in files:
        for n, lit in strings_in_file(p.read_text(encoding="utf-8")):
            total_strings += 1
            for mark, name in MARKS:
                if mark in lit:
                    bad.append((p.relative_to(ROOT), n, name, lit[:70]))
                    break

    if len(files) < 20 or total_strings < 200:
        print(f"  ✗ 装置坏了：只扫到 {len(files)} 个文件、{total_strings} 个字符串")
        return 1

    for path, n, name, lit in bad:
        print(f"  ✗ {path}:{n} 的字符串里有{name}：{lit}")
        print("      这类字符串是原样显示的（契约 §0.3），标记不会被渲染。"
              "强调用「」或改措辞。")

    if not bad:
        print(f"  ✓ {total_strings} 个字符串里没有渲染不出来的标记"
              f"（扫了 {len(files)} 个非测试 Go 文件）")
    return len(bad)


if __name__ == "__main__":
    sys.exit(1 if main() else 0)
