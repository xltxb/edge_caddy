#!/usr/bin/env python3
"""跑 go test，**永远留下失败现场**。

这个脚本存在的理由是一次真实的疏漏：2026-08-21 我用一行内联 python 统计
`go test ./... -json`，它只打印「通过=N 失败=M」。某次跑出「199/1」，
而我在同一个命令块里紧接着提交了 —— 那个 1 是什么，至今不知道，
后来六轮都没能复现。

**现场没留下，那次运行就等于没跑过。**

讽刺的是我跟前端 agent 讨论了好几轮「只看 N failed 不够，要看红在哪一条」，
而我自己的统计工具就是只输出计数的那种。**说得出一个形状，
跟在自己工具里认得出它，是两回事。**

跑法：python3 scripts/gotest.py [go test 的额外参数...]
"""
import json
import pathlib
import tempfile
import subprocess
import sys

def main():
    args = sys.argv[1:] or ["./..."]
    p = subprocess.run(["go", "test", *args, "-count=1", "-json"],
                       capture_output=True, text=True)

    passed, failed, skipped, out, nonjson = 0, [], [], {}, []
    # **包级 output（没有 Test 字段）也要留着。**
    #
    # 编译错误就走这一条：`go test -json` 把它包成 output 事件，
    # 但那些事件**没有 Test 字段**。原先只收集带 Test 的，于是编译错误
    # 被这个脚本自己过滤掉了 —— 它报「一条测试都没跑」而说不出为什么。
    #
    # 这跟 caddytest 那条恒为空的日志诊断是同一个错：写了一个诊断，
    # 而没验证它诊断得出东西。这次是在给一个新包写第一批测试时撞见的
    # （一个 declared and not used），第一次就撞上了。
    pkgout = {}
    # **编译错误是 stdout 上的 JSON，Action 叫 "build-output"。**
    #
    # 这一条我判断错过一次，值得记：我先跑了两个观测——
    # 一个说 stdout 里有那行错误，一个（用了 `2>&1 >/dev/null`）看起来说它在
    # stderr。**两个观测互相矛盾，而我只用了后一个**，据此写了一段解析 stderr
    # 的代码，跑出来 stderr 长度是 0。
    #
    # 正确的读法是第一个观测：它在 stdout，只是 Action 不是 "output"
    # 而是 "build-output"，被我原先的条件漏掉了。
    buildout = []
    for line in p.stdout.splitlines():
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            # 编译错误不是 JSON。它必须被看见——否则「0 失败」会是
            # 一句关于一次根本没发生的运行的陈述。
            nonjson.append(line)
            continue
        if e.get("Action") == "build-output":
            buildout.append(e.get("Output", ""))
            continue
        if not e.get("Test") and e.get("Action") == "output":
            pkgout.setdefault(e.get("Package", "?"), []).append(e.get("Output", ""))
        if e.get("Test"):
            if e["Action"] == "pass":
                passed += 1
            elif e["Action"] == "fail":
                failed.append((e["Package"], e["Test"]))
            elif e["Action"] == "skip":
                # **被跳过的测试要可见。**
                #
                # 只数 pass/fail 的话，给五条测试加上 t.Skip 会让「通过=200」
                # 变成「通过=195」——而那五条去哪了，没有任何信号。
                # 一个悄悄被跳过的测试跟没有测试一样，只是它还占着一个名字，
                # 让人以为那件事有人在看。
                skipped.append((e["Package"], e["Test"]))
            elif e["Action"] == "output":
                out.setdefault((e["Package"], e["Test"]), []).append(e["Output"])

    # **把失败现场同时写进文件。**
    #
    # 这个脚本存在的全部理由是留下现场，而我一次又一次用 `| tail -2`
    # 把它截掉——今天第六次。最后一次的代价是真的：一次偶发失败（244/2）
    # 重跑就绿了，而现场没了。
    #
    # 所以不靠「记得别截断」来修，**顺着那个习惯设计**：现场落盘，
    # 而指向它的那一行印在**最末尾**——`tail` 保得住的地方。
    report = []
    for key in failed:
        block = (f"\n{'='*70}\nFAIL  {key[0]}\n      {key[1]}\n{'='*70}\n"
                 + "".join(out.get(key, ["（没有输出——那本身就值得查）"])))
        print(block)
        report.append(block)

    if nonjson:
        print("\n非 JSON 输出：")
        print("\n".join(nonjson[:30]))

    # 一条测试都没跑、或者有失败时，包级输出往往是唯一说得出原因的东西。
    if (passed == 0 and not failed) or failed:
        for pkg, lines in pkgout.items():
            text = "".join(lines).strip()
            # 「ok / no test files」这类噪音不值得打印。
            if not text or text.startswith(("ok ", "?   ", "PASS")):
                continue
            print(f"\n包级输出  {pkg}:\n{text[:1500]}")

    # **有编译错误时，「通过=N 失败=0」是一句谎话。**
    #
    # 撞到过：往 api 包里加了一行类型不对的测试代码，整个包 43 条一条都没跑，
    # 而这里印的是「通过=248 失败=0」—— 退出码是非零的（下面 p.returncode
    # 那条管着），但**人读的是这一行**。我是靠记得「刚才是 291」才发现的，
    # 那不是装置在工作，那是我碰巧记得。
    #
    # 编译不过的包不会产生任何 fail 事件：它的测试从来没有开始。
    # 所以 len(failed) 是 0，而它诚实地报告了一个错误的问题。
    line = f"\n通过={passed} 失败={len(failed)}"
    if buildout:
        line += "  ✗ 有包编译不过，它们的测试一条都没跑（见下）"
    if skipped:
        line += f" **跳过={len(skipped)}**"
    print(line)
    for pkg, t in skipped:
        print(f"  skip  {pkg.split('/')[-1]}.{t}")
    if buildout:
        print("\n编译错误：")
        print("".join(buildout).strip()[:2000])
    elif p.stderr.strip():
        print("stderr:", p.stderr.strip()[:500])

    # **一条都没跑也要非零退出。**
    #
    # 前端 agent 那句：「0 失败」在一条都没跑的时候同样成立，
    # 而那两种情况的处置完全相反。
    #
    # 最常撞上的是手工跑单条时把名字打错：go test 印一行 no tests to run
    # 然后 exit 0，这里会打印「通过=0 失败=0」—— 而一个 && 链会带着
    # 这个「没问题」一路跑到 git commit。
    if report or nonjson or buildout:
        path = pathlib.Path(tempfile.gettempdir()) / "edge-gotest-last-failure.txt"
        try:
            path.write_text("".join(report) + "".join(nonjson) + "".join(buildout),
                            encoding="utf-8")
            print(f"\n现场已存：{path}")
        except OSError as e:
            print(f"\n（现场写不进文件：{e}）")

    if passed == 0 and not failed:
        # **判词要指对方向。** 前端 agent 在他脚本上撞到这个：
        # 「全部被跳过」被报成「装置可能坏了」，把人指向工具，
        # 而真相是命令行参数打错了。**判词指错方向跟不报一样费时间。**
        #
        # Go 这边实测过：t.Skip 是 Action:"skip"，退出码仍是 0，
        # 而只数 pass/fail 的统计会把它显示成「零条」。
        if skipped:
            print(f"✗ {len(skipped)} 条测试全部被跳过，一条都没真的跑 —— "
                  "看上面的 skip 列表，是谁跳过的、为什么。")
        else:
            print("✗ 一条测试都没跑 —— 名字打错了？包路径不对？"
                  "这不是「没问题」，是一次没有发生过的运行。")
        return 1
    # **判词放最后一行**，理由与 comments.py / unread.py 相同：
    # `| tail -1` 是最省事、因此最常见的读法，让它说真话比要求自己读全篇可靠。
    bad = bool(failed or nonjson or buildout or p.returncode != 0)
    print()
    if buildout:
        print("  ✗ 编译不过 —— 那些包的测试一条都没跑，这不是「没问题」")
    elif bad:
        print(f"  ✗ {len(failed)} 条失败（共跑了 {passed + len(failed)} 条）")
    else:
        print(f"  ✓ {passed} 条全过")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
