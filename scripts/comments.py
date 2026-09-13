#!/usr/bin/env python3
"""注释里那些**别人改了、这边不会收到通知**的东西。

五族，各查一件事（跑法见 scripts/README.md）：首句在不在讲已经不成立的事；
陈述外部行为的理由有没有挂在会通知你的东西上；说「某条探针盯着这里」的那条
探针在不在；引的契约小节与 ADR 还在不在、有没有被取代；这份索引还准不准。

下面这段讲的是第一族。

# 首句

扫「注释块的首句在讲一个已经不成立的东西」。

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


# 引用的三种写法，按仓库里 274 处的实际形态定的（不是想当然）：
#   契约 §0.7 / api-contract.md §0.7 / ADR-0009
CITE_SECTION = re.compile(r"(?:契约|api-contract\.md)\s*§\s*(\d+(?:\.\d+)*)")
CITE_ADR = re.compile(r"ADR-(\d{4})")

# 被取代的 ADR 自己会在状态行上说是谁取代了它：
#   - 状态：**已被 [ADR-0015](...) 取代**
#   **推翻 [ADR-0001](...) 的核心决定。**
# 两种写法都认 —— 只认一种的话，漏掉的那种会让整族检查对那几个 ADR 失明。
SUPERSEDED_BY = re.compile(r"已被\s*\[?ADR-(\d{4})\]?.{0,80}?取代")
SUPERSEDES = re.compile(r"(?:推翻|取代)\s*\[?ADR-(\d{4})\]?")

def contract_sections():
    """api-contract.md 里有编号的小节标题，只要编号。

    标题形如 `## 2. WebSocket` / `### 0.2.1 未知字段一律拒绝`，编号后面跟点
    或空格都有，所以两种都认。

    **只要编号，不要正文。** 这里曾经切出每一节的正文、还让父节吃下子节，
    那是给数字比对用的；那一层撤掉之后正文就没人读了，留着等于养一段
    没人验的代码。
    """
    doc = (ROOT / "docs/api-contract.md").read_text(encoding="utf-8")
    out = set()
    for ln in doc.split("\n"):
        if m := re.match(r"#{2,4}\s+(\d+(?:\.\d+)*)\.?\s+\S", ln):
            out.add(m.group(1))
    return out


def adr_index():
    """ADR 编号 -> 取代它的那个编号（没被取代就是 None）。

    **ADR 的失效方式是被取代，不是被删。** 只查文件在不在，等于对这一族
    引用里最常见的那种过期视而不见 —— 而 185 处引用里绝大多数是 ADR。
    """
    out, superseded = {}, {}
    for f in sorted((ROOT / "docs/adr").glob("*.md")):
        num = f.name[:4]
        out[num] = None
        head = "\n".join(f.read_text(encoding="utf-8").split("\n")[:6])
        if m := SUPERSEDED_BY.search(head):
            superseded[num] = m.group(1)
        for victim in SUPERSEDES.findall(head):
            if victim != num:
                superseded.setdefault(victim, num)
    for victim, winner in superseded.items():
        if victim in out:
            out[victim] = winner
    return out


def check_citations(paths):
    """注释里引的契约小节要存在，引的 ADR 不能是已经被取代的那个。

    **被引的那一方变了，引用的这一方不会收到任何通知。** 这是这个仓库
    反复栽的那一格（domain.md「对外的陈述没人能替你核对」）：

      · 一句「后端目前不校验格式」，在后端补上校验之后仍留在注释里
      · 一句「契约那张表承诺 detail 非空」，引用了一张自己没去更新的表
      · `render.go` 里「证书由主控集中签发（ADR-0001）」—— 而 ADR-0001 的
        核心决定已被 ADR-0015 推翻，主控早就不签发了

    两层，各自的边界写在下面，因为**这个检查覆盖的比它看起来的少得多**：

      小节存在  引的 §X.Y 必须在 api-contract.md 里有对应标题。
                只认 `契约 §N` 与 `api-contract.md §N`；**`api-contract §N`
                （不带 .md）那种写法有 23 处，一处也看不见**。
      ADR 时效  引的 ADR 必须存在，且不能是已被取代的那个 —— 除非同一个
                注释块里也提到了取代它的那个 ADR（那是知情地在讲历史，
                `certs/manager.go` 就是这么写的）。

    **它抓不住的**：不带 §号的引用（三次事故里有一次是这样）；前端目录
    （web/ 有自己的 check-comments.mjs）；scripts/ 与 docs/ 下的同型引用；
    以及**引对了小节号、而那一节里说的已经不是那回事**——这一层只证明
    「这个编号指向的东西还在、还没过期」，不证明内容还对得上。

    数字比对试过，撤掉了：77 处引用里只有 7 处真进了比对，而它会把
    issue 号（`#260`）、年份、RFC 号都当成契约里的值来查。覆盖面小、
    误报面大，留着不如不留。
    """
    secs = contract_sections()
    adrs = adr_index()
    bad = 0
    seen_sec = seen_adr = 0

    for f in paths:
        for ln, text in blocks(f):
            where = f"{f.relative_to(ROOT)}:{ln}"
            cited_adrs = set(CITE_ADR.findall(text))

            for num in set(CITE_SECTION.findall(text)):
                seen_sec += 1
                if num not in secs:
                    print(f"  ✗ {where}\n     引了契约 §{num}，而它不存在")
                    bad += 1

            for num in sorted(cited_adrs):
                seen_adr += 1
                if num not in adrs:
                    print(f"  ✗ {where}\n     引了 ADR-{num}，而 docs/adr/ 下没有它")
                    bad += 1
                    continue
                winner = adrs[num]
                # 同块提到了取代者 = 知情地在讲历史，放过。
                #
                # **取代者不一定写成 ADR-00NN。** store.go 写的是
                # 「见 docs/adr/0011-postgres-supersedes-sqlite.md（取代 ADR-0006）」
                # —— 它比谁都知情，而只认 `ADR-` 前缀的判据会把它报成过期
                # （第一版就是这么误报的）。文件名前缀那种写法也算数。
                aware = winner and (
                    winner in cited_adrs
                    or re.search(rf"(?:ADR-|/){winner}[-\b]", text)
                )
                if winner and not aware:
                    print(f"  ✗ {where}\n     把 ADR-{num} 当依据引，"
                          f"而它已被 ADR-{winner} 取代"
                          f"\n     要么改引 ADR-{winner}，要么在同一段里说清"
                          f"这里讲的是被推翻之前的事")
                    bad += 1

    # **判词要说得出自己核了什么。** 一句「引用都对得上」在只验了编号存在
    # 与时效的前提下，读起来像内容也有人担保了 —— 而读输出的人不会去读
    # 这个函数的 docstring。
    if not bad:
        print(f"  ✓ {seen_sec} 处契约小节引用指向存在的标题，"
              f"{seen_adr} 处 ADR 引用没有引到被取代的（只验编号，不验内容）")
    else:
        print("\n  被引的那一方变了，引用的这一方不会收到通知 —— "
              "所以这件事得让脚本替你记着。")
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
    ok = _self_test_citations() and ok
    print(f"\n  自检 {'通过' if ok else '失败'}")
    return 0 if ok else 1


def _self_test_citations():
    """引用那一族也要自证不瞎。

    **装置坏了看起来跟「全都对」一模一样**，而这一族有两种坏法，方向相反：

      解析不出东西  每处引用都判成「不存在」而全红 —— 吵，但看得见。
      什么都说有    每处引用都判成「在」而全绿 —— **安静，因此更危险**。

    第一版只防了前一种（`len(secs) < 20`），而那正是「破坏手段必须和断言
    对称」说的事：把每节正文都换成整篇，自检照样全 ✓。所以下面每一条都
    成对：既要认出该认的，也要认不出不该认的。
    """
    ok = True

    secs = contract_sections()
    if len(secs) < 20:
        print(f"  ✗ 只解析出 {len(secs)} 个契约小节 —— 引用检查的结果没有意义")
        ok = False
    elif "0.2.1" not in secs or "7" not in secs:
        print("  ✗ 解析漏掉了已知的小节编号（0.2.1 / 7）—— 引它们的地方会被误报")
        ok = False
    elif "0.99" in secs or "42" in secs:
        print("  ✗ 解析认出了不存在的小节编号 —— 那意味着它对什么都点头，"
              "引一个错编号也不会红")
        ok = False
    else:
        print(f"  ✓ 契约小节：认出 {len(secs)} 个，且不认不存在的编号")

    adrs = adr_index()
    if len(adrs) < 10:
        print(f"  ✗ 只找到 {len(adrs)} 个 ADR —— 时效检查的结果没有意义")
        ok = False
    elif adrs.get("0001") != "0015" or adrs.get("0006") != "0011":
        print("  ✗ 没认出已知的取代关系（0001→0015、0006→0011）—— "
              "引一个被推翻的 ADR 不会红")
        ok = False
    elif adrs.get("0010") is not None or adrs.get("0015") is not None:
        print("  ✗ 把没被取代的 ADR 也当成过期的 —— 那会把每一处引用都报红")
        ok = False
    else:
        print(f"  ✓ ADR 时效：{len(adrs)} 个，取代关系认得出也不乱认")

    return ok


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
    bad += check_citations(files)
    print()
    bad += check_index()

    # **判词放最后一行。**
    #
    # 这个脚本原先把失败印在最前面、把 ✓ 印在最后，于是 `| tail -1`
    # 永远看到一行绿的 —— 而那是最省事、因此最常见的读法。
    # 我今天一晚上就是这么读它的，于是它连续几次报错退出 1 而我没看见。
    #
    # **让偷懒的读法也是对的读法。** 靠人记得读全篇是靠不住的，
    # 而把结论放在人一定会看到的位置是结构性的。
    #
    # （这是「观测手段截断了它要看的东西」那一格的处置：
    # 与其要求自己别用 tail，不如让 tail 说真话。）
    total = len(found) + bad
    print()
    if total:
        print(f"  ✗ 共 {total} 处要改（首句讲历史 / 理由没锚 / 引用过期 / 索引对不上）")
    else:
        print(f"  ✓ 全部通过（扫了 {len(files)} 个文件）")
    return 1 if total else 0


if __name__ == "__main__":
    sys.exit(main())
