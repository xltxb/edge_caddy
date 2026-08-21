#!/usr/bin/env python3
"""改坏探针：证明测试拦得住，而且拦在点上。

每条探针把源码改坏一处，然后回答三个问题：

  1. 改动真的落进文件了吗？   —— 不生效的改坏会产生「测试很健壮」的假结论
  2. **目标测试真的跑了吗？**   —— 编译失败时一条都不跑，而那也表现为「没红」
  3. 它红了吗？
  4. **红的是不是我想验的那一条？**  —— 红了不等于验过了

第三问是这个脚本存在的主要理由。手工改坏时人只看得到「N failed」，
而红有三种打偏的方式，每一种都长得像验过了：

  - 新断言没被触发，旧断言先炸（探针触发得太早）
  - 改坏没生效，而结论碰巧正确（临时脚本的 replace 静默不匹配）
  - 改坏生效了、语义确实坏了，而那条测试的观测点恰好看不见它
    （改动落在一个输出不变的分支上——这一种最像验过了）

跑法：python3 scripts/probes.py [名字片段]

需要真 PostgreSQL 与 pinned Caddy，跟 go test ./... 一样。
"""
import json
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent


class Probe:
    def __init__(self, name, why, file, old, new, pkg, test, expect_line=None):
        self.name = name
        self.why = why          # 这条探针保护的是哪个不变量
        self.file = file
        self.old = old
        self.new = new
        self.pkg = pkg
        self.test = test        # 期望变红的测试
        self.expect_line = expect_line  # 期望的失败信息片段（可选，最精确的一问）


PROBES = [
    Probe(
        "排空-超时默认值",
        "timeout=0 当成「立刻放弃」的话，排空会静默地什么也不做，"
        "而结果看起来完全正常：drained=false、remaining 是真的数字",
        "internal/agent/drain.go",
        "\tif timeout <= 0 {\n\t\ttimeout = drainDeadline\n\t}\n",
        "",
        "./internal/agent/", "TestWaitDrainedTreatsZeroTimeoutAsDefault",
        "应当走默认值继续等",
    ),
    Probe(
        "排空-超时报真实剩余数",
        "回一个布尔答不了人接下来那个决定（现在能不能关机）："
        "是还剩 2 条可以直接关，还是还剩 8000 条得再等",
        "internal/agent/drain.go",
        "\t\t\treturn false, remaining\n",
        "\t\t\treturn false, 0\n",
        "./internal/agent/", "TestWaitDrainedReportsRemainingOnTimeout",
        "想要 7",
    ),
    Probe(
        "心跳不冲掉下线标记",
        "ADR-0014 的核心论据。分了两列之后没有任何东西证明那个效果达成了——"
        "它成立只是因为写这条 SQL 的人碰巧没写那一列",
        "internal/store/nodes.go",
        "SET last_hb_at = now(), status = $3::node_status, cfg_version = $2",
        "SET last_hb_at = now(), status = $3::node_status, cfg_version = $2, drained_at = NULL",
        "./internal/store/", "TestHeartbeatDoesNotClearDrainedMark",
        "这正是 ADR-0014 分两列要防的事",
    ),
    Probe(
        "官方包没有那三个模块-装置自检",
        "这是否定断言：list-modules 换了输出格式的话，三条「不存在」"
        "会因为什么都没匹配到而全绿——一个什么也没检查的检查器",
        "internal/caddytest/modules_test.go",
        'exec.Command(bin, "list-modules")',
        'exec.Command(bin, "version")',
        "./internal/caddytest/", "TestOfficialCaddyStillLacksTheModulesWeRoutedAround",
        "这份输出不是我们以为的东西",
    ),
    Probe(
        "路由表-装置自检",
        "同上：Routes() 返回空的话「没有多余端点」会静静地成立",
        "internal/api/routes_test.go",
        "\tr, _ := newServer(t)\n\tif n := len(r.Routes())",
        "\tr, _ := newServer(t)\n\tr = gin.New()\n\tif n := len(r.Routes())",
        "./internal/api/", "TestNoUndocumentedEndpoints",
        "这份路由表不是我们以为的东西",
    ),
    Probe(
        "回源CA被拒-断言不能比意图宽",
        "err != nil 是「有点什么不对」，而这条想证的是「证书被拒」。"
        "端口分配失败、握手超时全都会让它绿",
        "internal/pki/pki_test.go",
        '\tif err == nil {\n\t\tt.Fatal("回源 CA 签的证书不该能通过隧道的客户端校验',
        '\terr = fmt.Errorf("dial tcp 127.0.0.1:1: connect: connection refused")\n'
        '\tif err == nil {\n\t\tt.Fatal("回源 CA 签的证书不该能通过隧道的客户端校验',
        "./internal/pki/", "TestUpstreamCACannotAuthenticateToTunnel",
        "失败原因得是证书被拒",
    ),
    Probe(
        "下线-排空真的跑到了",
        "排空只在上一步真的摘掉了解析时才执行。没有假 DNS 服务商的话，"
        "这段代码在 e2e 里一次也走不到——只在「上一步失败」分支被测过等于没测",
        "internal/api/nodeops.go",
        "\tif !dnsRemoved {",
        "\tif true {",
        "./internal/e2e/", "TestDrainActuallyDrainsWhenDNSWasRemoved",
        "排空应当成功",
    ),
    Probe(
        "下线-排空要说清它的边界",
        "解析摘了但 DNS 有 TTL，一段时间内仍会有新连接。"
        "不说的话「已排空」就是第三句假话，而人会据此关机",
        "internal/api/nodeops.go",
        '"已建立的连接都已结束；解析缓存未过期前仍可能有新连接进来"',
        '"已排空"',
        "./internal/e2e/", "TestDrainActuallyDrainsWhenDNSWasRemoved",
        "要说清它的边界",
    ),
    Probe(
        "接入Token-成功之后才消耗",
        "查验与消耗一体的话，后面三步失败都会烧掉 Token——"
        "其中两步是主控自己的内部错误",
        "internal/tunnel/server.go",
        "\tspec, err := s.opt.Store.PeekEnrollToken(ctx, hello.GetToken())",
        "\tspec, err := s.opt.Store.PeekEnrollToken(ctx, hello.GetToken())\n"
        "\tif err == nil {\n\t\t_ = s.opt.Store.ConsumeEnrollToken(ctx, hello.GetToken())\n\t}",
        "./internal/e2e/", "TestDrainedNodeCannotEnrollWithAPreIssuedToken",
        "没有上线",
    ),
    Probe(
        "在线判定-不吞错误",
        "isOnline 吞掉解析错误的话，stayOffline（「下线之后连不回来」）"
        "会在 /nodes 整个坏掉时说「很好，它确实没连回来」",
        "internal/e2e/rig_test.go",
        '\tstatus, e := r.do("GET", "/nodes", nil)',
        '\tstatus, e := r.do("GET", "/nodes-broken", nil)',
        "./internal/e2e/", "TestDrainedNodeIsRefusedUntilRejoined",
        "在线判定拿不到数据时必须炸",
    ),
    Probe(
        "删路由-摘绑定不是删规则",
        "「摘掉绑定」和「规则整个没了」产生同一个观测。"
        "探针刻意放在 UnbindDomain **之后** —— 放在之前会让 unbound_rules "
        "那条先炸，红在别处，而那看起来跟验过了一模一样",
        "internal/api/config_res.go",
        "\tif err := s.store.DeleteRoute(ctx, domain); err != nil {",
        '\t_ = s.store.DeleteRule(ctx, "wl")\n'
        "\tif err := s.store.DeleteRoute(ctx, domain); err != nil {",
        "./internal/e2e/", "TestDeleteRouteUnbindsRules",
        "规则 wl 应当还在",
    ),
]


def run_test(pkg, test):
    """跑一个测试，返回 (跑到的测试名, 红了的, 全部输出)。

    `ran` 是第二问的依据：**编译失败时一条测试都不会跑**，而那时 `failed`
    同样是空的。两个世界产生同一个观测，结论却完全相反——一个是探针坏了，
    一个是测试拦不住。没有这一问，前者会被当成后者，
    而人会去改一条本来没问题的断言。

    它顺带挡住另外两件同样表现为「空」的事：目标测试被改名（`-run` 匹配不到，
    go test 照样 exit 0），以及被 skip 掉。
    """
    p = subprocess.run(
        ["go", "test", pkg, "-run", f"^{test}$", "-count=1", "-json"],
        cwd=ROOT, capture_output=True, text=True,
    )
    ran, failed, output = set(), set(), []
    for line in p.stdout.splitlines():
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            # 编译错误不是 JSON，它直接印在 stdout 上。留着进 output。
            output.append(line + "\n")
            continue
        if e.get("Test") and e.get("Action") in ("pass", "fail"):
            ran.add(e["Test"])
        if e.get("Action") == "fail" and e.get("Test"):
            failed.add(e["Test"])
        if e.get("Action") == "output":
            output.append(e.get("Output", ""))
    return ran, failed, "".join(output) + p.stderr


def main():
    only = sys.argv[1] if len(sys.argv) > 1 else ""
    probes = [p for p in PROBES if only in p.name]
    if not probes:
        print(f"没有名字含 {only!r} 的探针")
        return 1

    touched, results = [], []
    for p in probes:
        path = ROOT / p.file
        original = path.read_text(encoding="utf-8")

        # 第一问：改动落得进去吗。
        # 静默不匹配正是这个脚本要防的东西之一，所以它是硬错误。
        if p.old not in original:
            results.append((p, "改坏没匹配到", f"在 {p.file} 里找不到要替换的片段"))
            continue
        touched.append((path, original))
        try:
            path.write_text(original.replace(p.old, p.new, 1), encoding="utf-8")
            if p.new and p.new not in path.read_text(encoding="utf-8"):
                results.append((p, "改动没落进文件", "写回之后读不到新内容"))
                continue

            ran, failed, out = run_test(p.pkg, p.test)

            # 第二问（正面自检）：那条测试真的跑了吗。
            #
            # 编译失败、名字被改、被 skip —— 三件事都让 failed 为空，
            # 跟「拦不住」是同一个观测，而处置完全相反。
            if p.test not in ran:
                head = "\n     ".join(l for l in out.splitlines() if l.strip())
                results.append((p, "一条测试都没跑", (
                    f"{p.test} 没有出现在这次运行里（既没 pass 也没 fail）。\n"
                    "     **这说明探针本身坏了，不是测试拦不住** —— "
                    "多半是改坏造成了编译错误，也可能测试被改名或 skip 了。\n     "
                    + head[:500])))
                continue

            # 第三问：红了吗。
            if p.test not in failed:
                results.append((p, "没红", f"{p.test} 跑了，但在改坏之后仍然通过"))
                continue
            # 第四问：红在点上吗。
            if p.expect_line and p.expect_line not in out:
                results.append((p, "红在别处", (
                    f"{p.test} 确实红了，但失败信息里没有 {p.expect_line!r}。\n"
                    "     红了不等于验过了：可能是别的断言先炸，"
                    "也可能这条改坏根本没碰到你想验的那个分支。")))
                continue
            results.append((p, "ok", ""))
        finally:
            path.write_text(original, encoding="utf-8")

    ok = sum(1 for _, verdict, _ in results if verdict == "ok")
    for p, verdict, detail in results:
        mark = "✓" if verdict == "ok" else "✗"
        print(f"  {mark} {p.name}" + ("" if verdict == "ok" else f"  —— {verdict}"))
        if verdict != "ok":
            print(f"     {detail}")
            print(f"     它保护的是：{p.why}")

    # 收尾自检：源码必须还原干净。
    #
    # **一个会留下损伤的探针比没有探针更糟**——它不只是自己不可信，
    # 它让后面所有测试的结论也变得不可信，因为那些测试跑的不是真正的源码。
    dirty = [str(path.relative_to(ROOT)) for path, orig in touched
             if path.read_text(encoding="utf-8") != orig]
    print(f"\n  探针 {ok}/{len(results)} 通过")
    if dirty:
        print("\n  ✗ 源码没还原干净，工作区被污染了：")
        for d in dirty:
            print(f"      {d}")
        print("  在修好之前，后面所有测试跑的都不是真正的源码。")
        return 2
    return 0 if ok == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
