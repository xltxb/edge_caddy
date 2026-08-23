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


# 「陈述外部行为的理由」要挂在**会通知你**的东西上。
#
# 前端 agent 先做的这一条（他那边扫的是「陈述后端行为」，我这边扫的是
# 「陈述 Caddy / PostgreSQL 行为」——各自的「对方」不同）。
#
# **锚有三种，第三种是我一直在用而没说出口的：**
#
#   契约  —— 改了等于改接口，对方会知道
#   ADR   —— 改了要写 supersedes
#   测试  —— 改了会红，而且它是**自动的**
#
# 第三种最强，但它此前不可检：一条「由某个测试守着」的理由，
# 除非注释里点了那条测试的名字，否则跟没有锚一样。所以 ANCHOR 认 `Test\w+`
# ——把「有没有锚」变成一个可查的信号。
MENTIONS = re.compile(r"Caddy|官方包|PostgreSQL|Postgres|pgx|slog")
JUSTIFY = re.compile(r"因为|所以|理由|之所以|才|不然|否则|正是|这样")
ANCHOR = re.compile(r"ADR-\d|adr/\d|api-contract|契约\s*§|CONTEXT\.md|§\d|Test[A-Z]\w+")


def blocks(path):
    """按注释块切，块内再按句切。

    **句子级，不是块级。** 前端在这一步栽过：他第一版按块判，
    于是一个讲纯前端取舍的块因为顺带提了一句后端就被误报——
    两句无关，而检查器把它们算作一句。

    这个毛病他连撞三次，每次换一层尺度：单行看不见多行结构 →
    单行看不见同一块里的别的行 → 整块看不见句子边界。
    **每一次都是检查的粒度和被检查对象的结构不匹配。**
    """
    lines = path.read_text(encoding="utf-8").split("\n")
    i = 0
    while i < len(lines):
        if not lines[i].strip().startswith("//"):
            i += 1
            continue
        start, buf = i, []
        while i < len(lines) and lines[i].strip().startswith("//"):
            buf.append(lines[i].strip().lstrip("/").strip())
            i += 1
        yield start + 1, " ".join(buf)


def check_anchors(paths):
    """陈述外部行为的理由，有没有挂在会通知你的东西上。"""
    bad = 0
    for f in paths:
        if f.name.endswith("_test.go"):
            continue
        for ln, text in blocks(f):
            if ANCHOR.search(text):
                continue
            for sent in re.split(r"[。；\n]", text):
                if MENTIONS.search(sent) and JUSTIFY.search(sent) and len(sent) > 12:
                    print(f"  ✗ {f.relative_to(ROOT)}:{ln}\n     {sent.strip()[:90]}")
                    bad += 1
                    break
    if not bad:
        print("  ✓ 每条陈述外部行为的理由都挂着契约 / ADR / 测试")
    else:
        print("\n  把它挂到会通知你的东西上：契约（改了等于改接口）、"
              "ADR（改了要写 supersedes）、或者点名那条守着它的测试。")
    return bad


def check_backlinks(paths):
    """产品代码里说「某条探针盯着这里」的，那条探针必须真的存在。

    **这一族链接一直是单向的。** `probes.py` 里每条都写着它守着哪个不变量、
    改哪个文件——**探针知道它守着谁，而被守的那一方不知道**。
    改 `TouchHeartbeat` 那条 SQL 的人，此前完全不知道有一条探针盯着它。

    （前端 agent 先看见这个不对称：他的 `check-premises.mjs` 每条都标了
    「依赖处」指向代码，而代码那一侧一句都没说自己被守着。）

    反向链接自己也会过期：探针删了或改了名，而代码里那句「有一条盯着」还在
    ——**那是最坏的一种假话，它让人以为有保护**。所以这里查它。

    **反方向不查**：一条探针没有反向链接只是少个指路牌，不是假话。
    两个方向的失效后果不同，不该用同一条规则。

    判据限定在**含 probes.py 的那个注释块内部**的「」引用。第一版限定成
    「含连字符的引用」，而「心跳不冲掉下线标记」这条探针名恰好没有连字符，
    于是探针改名之后核对静静地全绿——**又一次粒度不匹配**，
    第二版放宽成「块内所有『』」，立刻把两句引用的措辞误报成探针名。

    两次都是**按字符特征猜**；对的做法是**按结构定位**——只认紧跟在
    「probes.py 的」后面的那一串。
    """
    probes = (ROOT / "scripts" / "probes.py").read_text(encoding="utf-8")
    names = set(re.findall(r'Probe\(\s*\n\s*"([^"]+)"', probes))
    if len(names) < 5:
        print(f"  ✗ 只从 probes.py 解析出 {len(names)} 条探针名 —— "
              "解析坏了，下面的核对没有意义")
        return 1

    bad, linked = 0, 0
    for f in paths:
        if f.name.endswith("_test.go"):
            continue
        for ln, text in blocks(f):
            if "probes.py" not in text:
                continue
            # **按结构定位，不按字符特征猜。** 只认紧跟在「probes.py 的」
            # 后面的那一串「」——同一个注释块里的别的「」是引用的措辞
            # （detail 文案、假想的改动），不是探针名。
            for run in re.findall(r"probes\.py 的((?:「[^」]+」)+)", text):
                for quoted in re.findall(r"「([^」]+)」", run):
                    linked += 1
                    if quoted not in names:
                        print(f"  ✗ {f.relative_to(ROOT)}:{ln} 说「{quoted}」"
                              "盯着它，而 probes.py 里没有这条探针")
                        bad += 1
    if not bad:
        print(f"  ✓ {linked} 处反向链接都指向真实存在的探针")
    return bad


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

    # **两个目录都要扫。**
    #
    # 这里原先只扫 scripts/，另外把 edge-node_test.sh 手工 add 进来——
    # 而 deploy/ 下后来又多了 cf-realip.sh，索引提到了它，
    # 检查却对它一无所知。**一个手工补丁挡住了一次，挡不住第二次。**
    real = {p.name
            for d in ("scripts", "deploy")
            for p in (ROOT / d).iterdir()
            if p.suffix in (".py", ".sh")}

    bad = 0
    for name in sorted(real):
        if name not in text:
            print(f"  ✗ {name} 存在，而索引里没有它")
            bad += 1
    for m in re.finditer(r'((?:scripts|deploy)/(\w[\w.-]*\.(?:py|sh)))', text):
        if m.group(2) not in real:
            # **把索引里写的那个路径原样回显**，不要拼一个。
            # 拼的话，一条写在 deploy/ 下的失效引用会被报成 scripts/ 下的，
            # 而人会去那个目录找，找不到，然后怀疑是检查器坏了。
            print(f"  ✗ 索引指着 {m.group(1)}，而它不在了")
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
    bad = check_anchors(files)
    print()
    bad += check_backlinks(files)
    print()
    bad += check_index()
    return 1 if (found or bad) else 0


if __name__ == "__main__":
    sys.exit(main())
