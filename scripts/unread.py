#!/usr/bin/env python3
"""扫「写了但没人读的东西」。

2026-08-21 手工跑过一次，三类各抓到一个真的：`traffic_samples` 整张表、
proto 的 `LogBatch`、契约里的 `GET /nodes/:id/logs`。

第三类当场固化成了测试（`TestEveryEndpointMentionedInContractIsAccountedFor`），
前两类当时是一次性的 —— 而**一个用完就丢的检查，等于只在写它的那一刻生效过一次**。

不做成 go test：它需要人判断（有些字段确实是为将来准备的），而一条需要人判断的
测试会在第一次误报之后被整体忽略，**包括它真正抓到的那些**。

跑法：python3 scripts/unread.py
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

# 已知且已有人认领的。**每条都要指向一张开着的单子** ——
# 一个没有出口的豁免列表会变成垃圾桶，而垃圾桶里迟早躺着一个真的遗漏。
#
# 只列**实际会被报出来**的。`traffic_samples.at`、`LogBatch.lines`、`LogLine.msg`
# 这些同样没人读，而它们不在这里 —— 见下面那段盲区说明。
KNOWN = {
    "traffic_samples.conns_total": "#25 这张表整个没人用，做还是删待拍板",
    "traffic_samples.req_total": "#25",
    "traffic_samples.origin_total": "#25",
    "AgentMsg.logs": "#26 节点日志整条链路都没有",
    "LogBatch.at_unix_ms": "#26",
}

# **这个筛子有已知的洞，说清楚它。**
#
# 判据是「这个名字在源码里出现过吗」，所以**短名和常见名会假阴性**：
# `at`、`id`、`lines`、`msg`、`level` 在别处一定出现，于是即便没人读它们
# 也不会被报出来。上面 KNOWN 里缺的那几条正是这样漏掉的。
#
# 不说的话，「0 项是新的」会被读成「确实没有新问题」，而实际是
# 「我这个筛子有洞」。**说清盲区是在保护真阴性**，跟说清误报是在保护真阳性
# 一样 —— 一次什么也没找到的扫描，要先说清它能找到什么。
BLIND_SPOT = "短名与常见名（at / id / msg / level / lines）会假阴性：判据是「名字出现过吗」"


def go_source():
    return "\n".join(
        p.read_text(encoding="utf-8")
        for d in ("internal", "cmd")
        for p in pathlib.Path(d).rglob("*.go")
    )


def scan_db_columns(gosrc):
    """建表/ALTER 里有，而任何 SELECT 都没出现过的列。

    **要处理 DROP COLUMN**：`deploys.target_count` 曾经是这个扫描的误报——
    它被后来的迁移删掉了，而扫描只看 CREATE/ADD。修掉误报比豁免它好，
    豁免会把一个「扫描不准」记成「这条不用管」。
    """
    cols = {}
    for f in sorted((ROOT / "internal/store/migrations").glob("*.up.sql")):
        src = f.read_text(encoding="utf-8")
        for m in re.finditer(r"CREATE TABLE (\w+)\s*\((.*?)\n\);", src, re.S):
            tbl, body = m.group(1), m.group(2)
            for line in body.split("\n"):
                line = line.strip()
                if (not line or line.startswith("--") or
                        line.upper().startswith(("PRIMARY", "FOREIGN", "UNIQUE",
                                                 "CHECK", "CONSTRAINT"))):
                    continue
                col = line.split()[0]
                if col.isidentifier():
                    cols[f"{tbl}.{col}"] = True
        for m in re.finditer(r"ALTER TABLE (\w+) ADD COLUMN (\w+)", src):
            cols[f"{m.group(1)}.{m.group(2)}"] = True
        for m in re.finditer(r"ALTER TABLE (\w+) DROP COLUMN (\w+)", src):
            cols.pop(f"{m.group(1)}.{m.group(2)}", None)

    out = []
    for key in sorted(cols):
        col = key.split(".", 1)[1]
        if not re.search(rf"SELECT[^;`]*\b{re.escape(col)}\b", gosrc, re.S | re.I):
            out.append(key)
    return out, len(cols)


def scan_proto_fields(gosrc):
    """proto 里定义了，而 Go 里既没 getter 也没被直接读写的字段。"""
    proto = (ROOT / "proto/edge/v1/edge.proto").read_text(encoding="utf-8")
    fields = set()
    for pat in (r"message (\w+) \{(.*?)\n\}", r"message (\w+) \{ ([^}]+) \}"):
        for m in re.finditer(pat, proto, re.S):
            msg, body = m.group(1), m.group(2)
            for line in body.split("\n"):
                line = line.split("//")[0].strip()
                mm = re.match(r"(?:repeated\s+)?[\w.]+\s+(\w+)\s*=\s*\d+;", line)
                if mm:
                    fields.add((msg, mm.group(1)))

    out = []
    for msg, f in sorted(fields):
        camel = "".join(w.capitalize() for w in f.split("_"))
        if ("Get" + camel) in gosrc:
            continue
        if re.search(rf"\b{camel}:\s", gosrc) or re.search(rf"\.{camel}\b", gosrc):
            continue
        out.append(f"{msg}.{f}")
    return out, len(fields)


def main():
    gosrc = ""
    # 装置自检：Go 源码得真的读进来了。
    # 没这一句的话，gosrc 为空会让**每一个**字段都显示为「没人读」——
    # 一个把所有东西都报成问题的扫描，跟一个什么都不报的一样没用。
    if len(gosrc) < 100_000 or "func " not in gosrc:
        print(f"✗ 只读到 {len(gosrc)} 字节 Go 源码 —— 这不是我们以为的东西，"
              "下面的结果全都没有意义")
        return 2

    findings, fresh = [], []
    for label, (items, total) in (
        ("DB 列", scan_db_columns(gosrc)),
        ("proto 字段", scan_proto_fields(gosrc)),
    ):
        print(f"\n  {label}（共 {total} 个）")
        if not items:
            print("    （都有人读）")
        for it in items:
            if it in KNOWN:
                print(f"    · {it}  —— 已知：{KNOWN[it]}")
            else:
                print(f"    ✗ {it}  —— 没人读，也没人认领")
                fresh.append(it)
        findings += items

    print(f"\n  共 {len(findings)} 项没人读，其中 {len(fresh)} 项是新的")
    print(f"  盲区：{BLIND_SPOT}")
    if fresh:
        print("\n  新出现的要当场判：它是欠条（开单子）、是死代码（删掉并说清为什么）、"
              "还是扫描不准（修扫描，别加豁免）。")
    return 1 if fresh else 0


if __name__ == "__main__":
    sys.exit(main())
