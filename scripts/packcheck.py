#!/usr/bin/env python3
"""这个包解到目标机器上，会不会弄坏它落脚的那个目录？

灰度上真实发生过：`/opt` 变成 `drwx------ 501 staff`——一台 macOS 的 UID
印在 Debian 上，**整台机器上任何非 root 用户都进不了 /opt**，控制台只是
第一个撞上的。成因是归档里每个条目都带着打包机器的 uid/gid 和模式，
而 GNU tar 以 root 解包时会把它们**一起恢复**。

所以这里查的不是「文件全不全」，是**这个归档对目标机器有什么意见**：

  1. 属主必须归零（uid/gid 都是 0，uname/gname 都是 root）。
     只归数字不够：GNU tar 优先按**名字**解析，目标机器上若恰好有个
     同名用户，文件就落到他头上。
  2. 模式必须 go+r，目录还要 go+x。否则解出来别人读不了。
  3. 顶层必须是**一个具名目录**，不能是 `./`。
     `./` 会把自己的属主和模式盖到解包的那个目录本身上；
     具名目录对上级一点意见都没有，而且忘了 --strip-components=1 时
     多出来的那一层**一眼看得见**，不是静默解不出东西。

**为什么不解析 `tar tzvf` 的输出。**

前端 agent 在他那版上栽过：他按位置数 `模式 链接数 属主 组 大小 日期 名字`，
而中文环境下日期是 `8月 23 11:07`——**三段**，于是「名字」从 `23` 开始，
49 条全被判成路径错。

> 数错字段不会报错，它给出一个看起来很确定的错答案。

而且 BSD tar 与 GNU tar 的列还不一样（属主/组是一列还是两列）。
Python 的 tarfile 直接读归档头里的字段，没有文本、没有 locale、
没有实现差异——**这一整类问题不是被处理了，是不存在**。
"""
import sys
import tarfile
from pathlib import Path


def check(path):
    problems = []
    with tarfile.open(path, "r:gz") as tf:
        members = tf.getmembers()

    # **先证明自己不瞎。** 一个空归档会让下面每一条都平静地通过。
    if len(members) < 3:
        return [f"装置坏了或包是空的：{path.name} 里只有 {len(members)} 个条目"], 0

    tops = {m.name.split("/")[0] for m in members}
    if "." in tops:
        problems.append(
            "顶层有 `./` 条目 —— 它的属主和模式会盖到解包的那个目录本身上。"
            "改成一个具名顶层目录")
    if len(tops) != 1:
        problems.append(f"顶层不止一项：{sorted(tops)} —— 解包会在目标目录里散落多个条目")

    for m in members:
        if (m.uid, m.gid) != (0, 0) or m.uname not in ("", "root") or m.gname not in ("", "root"):
            problems.append(
                f"{m.name}：属主是 {m.uname or m.uid}/{m.gname or m.gid}，不是 root/root"
                " —— 以 root 解包会把这个身份印到目标机器上")
            break  # 一条就够，49 条一样的噪音会淹掉别的问题
    for m in members:
        need = 0o755 if m.isdir() else 0o444
        if m.mode & need != need:
            kind = "目录" if m.isdir() else "文件"
            problems.append(f"{m.name}：{kind}模式 {m.mode:04o}，别人读不了"
                            f"（{kind}至少要 {need:04o} 里的 go 位）")
            break
    return problems, len(members)


def main(argv):
    paths = [Path(a) for a in argv[1:]]
    if not paths:
        print("  ✗ 没给包 —— 用法：packcheck.py <tar.gz>...")
        return 1
    bad = 0
    for p in paths:
        if not p.exists():
            print(f"  ✗ {p} 不存在")
            bad += 1
            continue
        problems, n = check(p)
        for msg in problems:
            print(f"  ✗ {p.name}: {msg}")
        bad += len(problems)
        if not problems:
            print(f"  ✓ {p.name}：{n} 个条目，属主 root/root、模式 go+rX、顶层具名目录")
    return bad


if __name__ == "__main__":
    sys.exit(1 if main(sys.argv) else 0)
