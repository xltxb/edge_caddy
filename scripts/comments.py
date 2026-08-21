#!/usr/bin/env python3
"""扫「注释块的首句在讲一个已经不成立的东西」。

**一个从上往下读的人，第一句读到什么就先信什么。** 把复盘放在结论前面，
等于让读者先装载一遍错的，然后指望他读到第三段时改回来。

这跟「注释过期」不是一回事：这里每一句单独看都是真的（「原先确实是那么写的」），
问题在**位置**。前端 agent 撞到同一族的五处，我这边两处。

判据刻意收得很紧：**只看注释块的首行**。历史写在后面是允许的，
而且这一整个仓库都在这么做——复盘有价值，它只是不该占第一句。

跑法：python3 scripts/comments.py [--self-test]
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

# **一个词能不能进这张表，看它「只」在那个意思上出现。**
#
# 前端 agent 的第一版词表里有「是假的」，当场撞上一句正常措辞
# （「格式正确而**意思是假的**值」——那讲的是现在，不是一次改正）。
#
# 这跟他第一遍用后端的词汇扫自己的欠条（11 处全误报）是同一件事的两端：
# **词表太生疏会全空，词表太贴近会撞上正常措辞。**
HISTORY_WORDS = r"原先|曾经|第一版|第二版|早先|一度|我改过"

# 排除：这些词后面跟着的是「现在怎样」而不是「过去怎样」。
# 目前为空 —— 留着这个口子是因为词表迟早会遇到第一个反例，
# 而那时该加豁免并写清理由，不是把词删掉（删掉会连真阳性一起丢）。
EXEMPT: dict[str, str] = {}


def scan(paths):
    hist = re.compile(HISTORY_WORDS)
    found = []
    for f in paths:
        lines = f.read_text(encoding="utf-8").split("\n")
        for i, line in enumerate(lines):
            t = line.strip()
            if not t.startswith("//"):
                continue
            # 只看注释块的第一行。
            if i > 0 and lines[i - 1].strip().startswith("//"):
                continue
            if not hist.search(t):
                continue
            key = f"{f.relative_to(ROOT)}:{i + 1}"
            if key in EXEMPT:
                continue
            found.append((key, t))
    return found


def check_index():
    """scripts/README.md 那份索引必须与真实存在的脚本**双向**一致。

    一份手写的索引正是最会过期的东西，而**索引过期不会有任何东西报错**——
    它只在有人照它做的时候才显形。

    双向是关键。只查「索引提到的都存在」的话，「加了脚本忘了写索引」
    永远不会被发现——而那正是索引最常见的坏法。

    （前端 agent 先做的这一条，他指出它跟 contractEndpoints 那张表是同一个
    形状：「契约有而路由没有」和「路由有而契约没有」都要查。）
    """
    readme = ROOT / "scripts" / "README.md"
    if not readme.exists():
        print("  ✗ scripts/README.md 不见了 —— 那份索引是这些脚本唯一的入口")
        return 1
    text = readme.read_text(encoding="utf-8")

    real = {p.name for p in (ROOT / "scripts").iterdir()
            if p.suffix in (".py", ".sh") and p.name != "README.md"}
    real.add("edge-node_test.sh")  # 它住在 deploy/，但索引里该有它

    bad = 0
    for name in sorted(real):
        if name not in text:
            print(f"  ✗ {name} 存在，而索引里没有它")
            bad += 1
    for m in re.finditer(r'scripts/(\w[\w.-]*\.(?:py|sh))', text):
        if m.group(1) not in real:
            print(f"  ✗ 索引指着 scripts/{m.group(1)}，而它不在了")
            bad += 1
    if not bad:
        print(f"  ✓ 索引与 {len(real)} 个脚本双向一致")
    return bad


def go_files():
    return sorted(
        p for d in ("internal", "cmd")
        for p in (ROOT / d).rglob("*.go")
    )


def self_test():
    """脚本自己先证明不瞎：造一个该命中的和一个不该命中的。"""
    import tempfile
    ok = True
    with tempfile.TemporaryDirectory() as d:
        good = pathlib.Path(d) / "good.go"
        good.write_text(
            "// Foo 做这件事。\n"
            "//\n"
            "// 这里原先是另一种写法。\n"
            "func Foo() {}\n", encoding="utf-8")
        bad = pathlib.Path(d) / "bad.go"
        bad.write_text(
            "// 这里原先写着另一句话。\n"
            "// Foo 现在做这件事。\n"
            "func Foo() {}\n", encoding="utf-8")

        global ROOT
        saved, ROOT = ROOT, pathlib.Path(d)
        try:
            hits = {k.split(":")[0] for k, _ in scan([good, bad])}
        finally:
            ROOT = saved

        if "bad.go" not in hits:
            print("  ✗ 首句讲历史的没被抓到"); ok = False
        else:
            print("  ✓ 首句讲历史的会被抓到")
        if "good.go" in hits:
            print("  ✗ 历史写在后面的被误报了 —— 那是允许的写法"); ok = False
        else:
            print("  ✓ 历史写在后面的不误报")
    print(f"\n  自检 {'通过' if ok else '失败'}")
    return 0 if ok else 1


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--self-test":
        return self_test()

    files = go_files()
    # 装置自检：源码得真的扫到了。
    if len(files) < 20:
        print(f"✗ 只找到 {len(files)} 个 Go 文件 —— 下面的结果没有意义")
        return 2

    found = scan(files)
    for key, text in found:
        print(f"  ✗ {key}\n     {text[:110]}")
    print(f"\n  扫了 {len(files)} 个文件，{len(found)} 处首句在讲历史")
    if found:
        print("\n  把结论提到第一句，复盘放它后面 —— 复盘有价值，"
              "它只是不该占第一句。")

    print()
    bad = check_index()
    return 1 if (found or bad) else 0


if __name__ == "__main__":
    sys.exit(main())
