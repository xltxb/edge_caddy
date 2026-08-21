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
import subprocess
import sys

def main():
    args = sys.argv[1:] or ["./..."]
    p = subprocess.run(["go", "test", *args, "-count=1", "-json"],
                       capture_output=True, text=True)

    passed, failed, skipped, out, nonjson = 0, [], [], {}, []
    for line in p.stdout.splitlines():
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            # 编译错误不是 JSON。它必须被看见——否则「0 失败」会是
            # 一句关于一次根本没发生的运行的陈述。
            nonjson.append(line)
            continue
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

    for key in failed:
        print(f"\n{'='*70}\nFAIL  {key[0]}\n      {key[1]}\n{'='*70}")
        print("".join(out.get(key, ["（没有输出——那本身就值得查）"])))

    if nonjson:
        print("\n非 JSON 输出（多半是编译错误）：")
        print("\n".join(nonjson[:30]))

    line = f"\n通过={passed} 失败={len(failed)}"
    if skipped:
        line += f" **跳过={len(skipped)}**"
    print(line)
    for pkg, t in skipped:
        print(f"  skip  {pkg.split('/')[-1]}.{t}")
    if p.stderr.strip():
        print("stderr:", p.stderr.strip()[:500])

    # **一条都没跑也要非零退出。**
    #
    # 前端 agent 那句：「0 失败」在一条都没跑的时候同样成立，
    # 而那两种情况的处置完全相反。
    #
    # 最常撞上的是手工跑单条时把名字打错：go test 印一行 no tests to run
    # 然后 exit 0，这里会打印「通过=0 失败=0」—— 而一个 && 链会带着
    # 这个「没问题」一路跑到 git commit。
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
    return 1 if (failed or nonjson or p.returncode != 0) else 0


if __name__ == "__main__":
    sys.exit(main())
