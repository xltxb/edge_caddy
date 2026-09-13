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
    # 「导入证书-auto_renew 必须关」删于 ADR-0015。
    #
    # 它保护的是：留成 true 的话，续期扫描会挑中这张导入的证书，
    # **主控用 ACME 重签一张覆盖掉它**。而 ACME 和续期扫描都被移除了 ——
    # **那个威胁模型不再存在**。
    #
    # 删掉而不是让它留着「反正也不红」：一条永远不会红的探针，
    # 在清单上和一条真正在守着什么的探针长得一模一样，
    # 而它会让「16/16 通过」这个数字虚高。
    #
    # 这一条是 ADR-0015 落地时**探针自己报出来的**：改完之后它没红，
    # 而 probes.py 把「跑了但没红」单独报成一档。**如果它只报「通过」，
    # 这条死探针会一直躺在清单里。**
    Probe(
        "导入证书-域名必须对得上",
        "证书本身完全有效，只是签的是别的域名 —— 浏览器报名称不符，"
        "而人会去查 DNS、查 Caddy，因为「证书是有效的」",
        "internal/certs/import.go",
        "\tif err := leaf.VerifyHostname(domain); err != nil {",
        "\tif false {",
        "./internal/certs/", "TestImportRejectsWrongDomain",
        "必须拒绝",
    ),
    Probe(
        "节点日志-level 用契约的四个小写值",
        "slog 的 String() 给的是 INFO/WARN，直接透出去会让前端在契约列的"
        "四个已知取值之外再见到四个大写的 —— 而它长得像是「多了几个取值」，"
        "不像是「有人没照契约来」",
        "internal/agent/logbuf.go",
        '\t\treturn "debug"',
        "\t\treturn l.String()",
        "./internal/agent/", "TestLevelNamesMatchContract",
        "想要",
    ),
    Probe(
        "节点日志-送失败要放回缓冲",
        "一条重复的日志读得出来是重复的，一条缺失的日志读起来跟"
        "「那时什么也没发生」一模一样",
        "internal/agent/logbuf.go",
        "\tb.lines = append(lines, b.lines...)",
        "\t_ = lines",
        "./internal/agent/", "TestPutBackKeepsOrder",
        "应当有三条",
    ),
    Probe(
        "流量采样-报数不齐就不记",
        "偏低的样本在 24 小时后会成为同比的分母，产生一个假的巨大涨幅。"
        "而一个 +340% 比一个 null 危险得多：null 会让人去查，具体的百分比不会",
        "internal/traffic/traffic.go",
        "\tif reported < want {",
        "\tif false {",
        "./internal/traffic/", "TestSampleIsSkippedWhenNodesAreMissing",
        "不该记下样本",
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
    Probe(
        "DNSPod-空轮换不清记录",
        "一个节点都不在轮换里时把记录删光 = 主动制造 NXDOMAIN，"
        "而这多半只是一次短暂的全体离线（主控重启就够了）。"
        "两个 Cloudflare 适配一直守着这条，DNSPod 漏了三年（issue #36）。"
        "改坏之后 Sync 会一路走到删除循环，症状是「没报错」——"
        "而「没报错」正是这个 bug 当初能活下来的原因",
        "internal/dnsctl/dnspod.go",
        '\tif len(want) == 0 {\n'
        '\t\treturn emptyRotationErr("没有任何节点在解析轮换里，本次不改动 DNS 记录")\n'
        "\t}\n",
        "",
        "./internal/dnsctl/", "TestDNSPodKeepsRecordsWhenNothingIsInRotation",
        "一个节点都没有时该明确报错",
    ),
    Probe(
        "回源证书-没人下发也会续",
        "ADR-0009 说回源叶子 24 小时、续期通道「隧道，自动」，"
        "而续期原先只挂在下发路径上——真实条件是「没人点下发」超过 24 小时。"
        "改坏之后循环照常转，只是一趟都不干活：全绿、无日志、无事件，"
        "直到 24 小时后回源全断（issue #39）",
        "internal/deploy/renew.go",
        "\t\ts.renewUpstreamCerts(ctx)\n",
        "",
        "./internal/deploy/", "TestUpstreamCertsRenewWithNobodyDeploying",
        "一张回源证书都没收到",
    ),
    Probe(
        "隧道-发送串行化",
        "gRPC 的流不允许并发 Send，而 Agent 有五条并发的发送路径。"
        "去掉锁之后一切照常：消息条数一样、错误路径一样、测试里除了重叠数"
        "没有任何观测会变——数次数的断言在这儿是看不见的（issue #37）",
        "internal/agent/tunnelwriter.go",
        "\tw.mu.Lock()\n\tdefer w.mu.Unlock()\n",
        "",
        "./internal/agent/", "TestAgentSendPathsShareOneSerializedWriter",
        "在同一条流上重叠了",
    ),
    Probe(
        "HSTS-只在 :443 上发",
        "tlsRoutes 曾经写好了却没有调用方，于是 HSTS 在两台 server 上都不出现，"
        "而守着它的那条 e2e 只断言了「明文响应不带 HSTS」——一条否定断言，"
        "渲染器根本没挂 handler 时同样是绿的（issue #40）",
        "internal/render/render.go",
        "\t\t\t\"routes\":          tlsRoutes(caddyRoutes, pol),\n",
        "\t\t\t\"routes\":          caddyRoutes,\n",
        "./internal/render/", "TestHSTSRendersOnTheTLSServerOnly",
        "却没有 Strict-Transport-Security",
    ),
    Probe(
        "重连-一次连接一份循环",
        "循环用调用方那个进程级 ctx 的话，隧道断开时它们不退出，"
        "而 main 紧接着重连再起一份。泄漏的那份发不到主控（流已经死了），"
        "所以主控侧看不见——症状只在节点的 CPU 与 fd 上（issue #42）",
        "internal/agent/agent.go",
        "a.heartbeatLoop(connCtx, out)",
        "a.heartbeatLoop(ctx, out)",
        "./internal/agent/", "TestServeStopsItsLoopsWhenTheTunnelDrops",
        "serve 没有返回",
    ),
    Probe(
        "下发-合入是一个事务",
        "七步散着跑时，只有第一步的失败会中止流水线（#31 的修复），"
        "后面五步各自只 log.Error 然后继续；而合入 live 自己也是逐条 Upsert。"
        "把事务拆掉之后 live 会被半更新，**而界面说这次下发没成**（issue #43）",
        "internal/store/commit.go",
        "\tdefer func() { _ = tx.Rollback(ctx) }()",
        "\tdefer func() { _ = tx.Commit(ctx) }()",
        "./internal/deploy/", "TestCommitIsAtomicAcrossRoutes",
        "live 被半更新了",
    ),
    Probe(
        "回滚-整批写草稿",
        "逐条写、中途失败就地返回的话，工作台里亮着前几条而响应里一个 "
        "res_key 都不报——人接着发出去的是半个回滚（issue #44）。"
        "改坏必须是「根本没有事务」：PG 在语句出错时会自己中止整个事务，"
        "所以把 Rollback 换成 Commit 是看不出区别的（试过，没红）",
        "internal/store/drafts.go",
        "putDraft(ctx, tx, resKey, patches[resKey], by)",
        "putDraft(ctx, s.Pool, resKey, patches[resKey], by)",
        "./internal/store/", "TestPutDraftsIsAllOrNothing",
        "工作台里会亮着半个回滚",
    ),
    Probe(
        "回源率-分母不重复计数",
        "caddy_http_requests_total 按 handler 各记一次，把所有行相加会让分母"
        "翻倍、回源率腰斩。而两个数一起错的时候指标之间仍然自洽——"
        "判据只能是「我发了几个请求」（issue #41）",
        "internal/agent/metrics.go",
        "\t\tcase strings.Contains(labels, `handler=\"static_response\"`):\n\t\t\treq += uint64(v)\n",
        "\t\tdefault:\n\t\t\treq += uint64(v)\n",
        "./internal/agent/", "TestRequestTotalsCountEachRequestOnce",
        "req_total 报的是",
    ),
    Probe(
        "回源率-分子减掉被拒的",
        "在 forward_auth 处被拒的请求也记在 handler=\"reverse_proxy\" 上，"
        "而它一个字节都没到源站。Caddy 的计数器分不出这两件事，"
        "校验端点自己分得出（issue #41）",
        "internal/agent/metrics.go",
        "out.OriginTotal = subFloor(origin, m.verifyDenied())",
        "out.OriginTotal = origin",
        "./internal/agent/", "TestRequestTotalsCountEachRequestOnce",
        "origin_total 报的是",
    ),
    Probe(
        "草稿-必须是对象",
        "丢掉 Unmarshal 的错误，非对象 JSON 就存得进 jsonb 列。代价不在写入这一步："
        "之后 mergeInto 会失败，Deploy 与 Preview 双双 500，而人在界面上找不到"
        "入口删它——一个从界面上解不开的死局（issue #58）",
        "internal/store/drafts.go",
        "\tm, err := asObject(patch)\n\tif err != nil {\n\t\treturn fmt.Errorf(\"草稿 %s: %w\", resKey, err)\n\t}",
        "\tvar m map[string]any\n\t_ = json.Unmarshal(patch, &m)",
        "./internal/store/", "TestPutDraftRejectsNonObjectPatch",
        "应当被拒，而它存进去了",
    ),
    Probe(
        "DNSPod-服务商不回权重时也幂等",
        "Weight 是 *int（权重是付费套餐特性）。写成 `!= nil && ==` 的话 nil 时恒假，"
        "每一次自愈都对全部记录发一轮 Modify。症状离原因很远：撞上接口频率限制之后，"
        "人看到的是「摘除偶尔失败」（issue #54）",
        "internal/dnsctl/dnspod.go",
        "if cur.Weight == nil || *cur.Weight == weight {",
        "if cur.Weight != nil && *cur.Weight == weight {",
        "./internal/dnsctl/", "TestDNSPodSyncIsIdempotentWhenProviderOmitsWeight",
        "第二次 Sync 又写了",
    ),
    Probe(
        "WS-会话没了就断开",
        "鉴权只发生在升级那一刻的话，登出之后那条连接仍然在推节点状态与事件流，"
        "直到浏览器自己关掉。ADR-0013 说控制台访问 = 网络 + 会话，"
        "而会话那条腿在 WS 上只站了一瞬间（issue #64）",
        "internal/ws/handler.go",
        "\t\t\t\tif opt.StillValid != nil && !opt.StillValid(r) {",
        "\t\t\t\tif false {",
        "./internal/ws/", "TestConnectionClosesWhenTheSessionGoesAway",
        "连接却还活着",
    ),
    Probe(
        "重连次数-一次查回全体",
        "逐台问是 N 个往返，而节点列表页是常驻轮询的；events 又只写不清，"
        "行数随时间线性涨。ctx 一超时，reconnects_1h 会从某一行开始整片变 null"
        "——恰好是这个字段最该说话的时候（issue #59）",
        "internal/store/events.go",
        "GROUP BY node_id`,",
        "GROUP BY node_id LIMIT 1`,",
        "./internal/store/", "TestCountReconnectsByNodeAnswersForEveryoneAtOnce",
        "数出来是",
    ),
    Probe(
        "回源证书-两个文件一起落地",
        "两次直接覆写的话，中间被打断就留下「新证书配旧私钥」。"
        "Caddy 下次自启动会因为这对不上而整份失败，而报错与「上次写证书被打断」"
        "毫无表面关联（issue #62）",
        "internal/agent/agent.go",
        "\t\t_ = os.Remove(certTmp)\n\t\treturn fmt.Errorf(\"写入回源私钥: %w\", err)",
        "\t\t_ = os.Rename(certTmp, certPath)\n\t\treturn fmt.Errorf(\"写入回源私钥: %w\", err)",
        "./internal/agent/", "TestUpstreamCertSurvivesAFailedWrite",
        "两个文件没能都落地时",
    ),
    Probe(
        "下发-不占读循环",
        "下发同步跑的话，一次慢重载期间这条隧道读不到任何东西——"
        "主控推不下来配置也探不了活，于是一台正在正常下发的机器被判成不可达。"
        "紧邻的 Drain 分支早就因为同一条理由改成了 go（issue #61）",
        "internal/agent/agent.go",
        "\t\t\twg.Add(1)\n\t\t\tgo func() { defer wg.Done(); a.handlePush(connCtx, out, m.Push) }()",
        "\t\t\ta.handlePush(connCtx, out, m.Push)",
        "./internal/agent/", "TestProbeIsAnsweredWhileADeployIsStillRunning",
        "探活就一直没人回",
    ),
    Probe(
        "改服务商-真故障要进 error 日志",
        "把「下游真出事了」也算成预期结果的话，网络、凭证、服务商挂了都不再"
        "进 error 日志——而把配置选择记成 error 同样有害：日志里的 error 变得"
        "不值得看，真出事那次就没人注意得到",
        "internal/api/settings.go",
        'return false, "服务商设置已保存，但同步到服务商失败：" + err.Error(), false',
        'return false, "服务商设置已保存，但同步到服务商失败：" + err.Error(), true',
        "./internal/api/", "TestProviderSyncTellsEmptyRotationApartFromAFailure",
        "要不要进 error 日志",
    ),
    Probe(
        "流量采样-当下的界线由 health 给",
        "自己写死一个常数的话，它与 health 判 down 的窗口就是两套判据："
        "界线紧了，样本算陈旧而节点还在 want 里，整分钟的采样被静默跳过；"
        "松了，已判离线的机器的旧数字被加进当下的汇总（issue #48）",
        "internal/traffic/traffic.go",
        "\tstaleAfter := h.StaleAfter()",
        "\tstaleAfter := defaultStaleAfter",
        "./internal/traffic/", "TestFreshnessComesFromHealthNotAConstant",
        "整分钟的采样被静默跳过",
    ),
    Probe(
        "判离线-对外说的窗口要盖得住",
        "StaleAfter 少算一个周期的话，采样会在 health 判 down 之前就把样本"
        "当成陈旧——reported < want，整分钟的采样被跳过，而没有任何报错。"
        "tick 的相位与心跳到达的时刻无关，最坏要多等一个周期",
        "internal/health/health.go",
        "\treturn m.Interval * time.Duration(m.Threshold+1)",
        "\treturn m.Interval * time.Duration(m.Threshold)",
        "./internal/health/", "TestStaleAfterIsTheTightBoundOnGoingDown",
        "采样被静默跳过",
    ),
    Probe(
        "登录限速-成功不中和 IP 维度",
        "succeed 把 IP 那一档也清掉的话，攻击者拿自己的账号夹在中间就能把它"
        "中和：对 A 试 4 次、自己登一次、对 B 再试 4 次……IP 维度存在的全部"
        "理由就是挡这个。破坏点在 handleLogin 传什么 key，不在限速器内部",
        "internal/api/auth.go",
        "\ts.logins.succeed(userKey)",
        "\ts.logins.succeed(ipKey)",
        "./internal/api/", "TestASuccessfulLoginDoesNotNeutralizeTheIPCounter",
        "中和掉了",
    ),
    Probe(
        "登录限速-表不能只增不减",
        "recent 过滤完不写回、空了不删键的话，每个来过的 IP 永久占一格——"
        "而登录端点不需要鉴权，这张表是一条没人看的内存曲线",
        "internal/api/loginrate.go",
        "\tif len(kept) == 0 {\n\t\tdelete(l.fails, k)\n\t\treturn nil\n\t}\n\tl.fails[k] = kept",
        "\tl.fails[k] = kept",
        "./internal/api/", "TestLimiterDoesNotGrowOnEveryAttempt",
        "无界增长",
    ),
    Probe(
        "节点ID-格式后端也要拦",
        "node_id 会被写进隧道证书的 CN（ADR-0009）、拼进九处 URL 路径、"
        "进 DNS 记录的比对键。控制台挡这一条，而直改路（ops-bot、批量脚本）"
        "绕过控制台——签发 Token 之后人就去跑安装脚本了，那不是代价为零的重来",
        "internal/api/nodes.go",
        "\tif !model.ValidResourceID(req.NodeID) {",
        "\tif false {",
        "./internal/api/", "TestTokenRefusesMalformedNodeID",
        "被接受了",
    ),
    Probe(
        "规则ID-格式后端也要拦",
        "PUT 是 upsert，这条路会创建规则，而 id 会被拼进 URL 路径——"
        "删除走的是同一个路径。契约 §0.7 把格式定成接口的一部分",
        "internal/api/config_res.go",
        "\tif !model.ValidResourceID(id) {",
        "\tif false {",
        "./internal/api/", "TestPutRuleRefusesMalformedID",
        "被接受了",
    ),
    Probe(
        "只建不覆盖-要原子",
        "先查再写的话两句之间有窗口：两个人同时新建同一个 id，两句查询都说没有，"
        "后写的把先写的整个换掉还回 code: 0——那正是 #70 要挡的「静默覆盖别人"
        "配好的规则」本身，而契约 §6.2 承诺的是「一个字节都不写」",
        "internal/store/rules.go",
        "\t\t ON CONFLICT (id) DO NOTHING`,",
        "\t\t ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name`,",
        "./internal/store/", "TestInsertRuleIfAbsentIsAtomic",
        "一个字节都不写",
    ),
    Probe(
        "解析同步-panic 不锁死编排器",
        "手工配对的 Lock/Unlock 夹着 syncLocked 的话，那底下任何一次 panic 的"
        "后果都是进程级的：gin 的 Recovery 把请求救回来、控制台一切正常，而 o.mu "
        "永不释放——此后改权重、点开关、心跳摘挂、自愈全部永久阻塞，不超时也不报错",
        "internal/dnsops/orchestrator.go",
        "\to.mu.Lock()\n\tdefer o.mu.Unlock()\n\to.coalesceMu.Lock()\n\to.pending = nil\n\to.coalesceMu.Unlock()\n\n\treturn o.syncLocked(ctx, nil)",
        "\to.mu.Lock()\n\to.coalesceMu.Lock()\n\to.pending = nil\n\to.coalesceMu.Unlock()\n\n\terr = o.syncLocked(ctx, nil)\n\to.mu.Unlock()\n\treturn err",
        "./internal/dnsops/", "TestPanicDoesNotWedgeTheOrchestrator",
        "再也不返回了",
    ),
    Probe(
        "下发-全局策略也要落回 live",
        "CommitDeploy 只合入路由和规则的话，`global:` 的版本照样推进、草稿照样删，"
        "只有 live 那行 spec 没动——下一次任何下发把旧值推回去，而工作台上没有草稿、"
        "版本是新的、基线是新的，没有一个页面会说这件事。issue #31 的形状",
        "internal/store/commit.go",
        "\t\tif err := upsertPolicy(ctx, tx, p); err != nil {",
        "\t\tif err := error(nil); err != nil {",
        "./internal/deploy/", "TestGlobalPolicyLandsInLive",
        "这次改动从此哪儿都不在",
    ),
    Probe(
        "整批写草稿-非对象也要拦",
        "`[1,2]` / `null` 是合法 JSON，jsonb 列放行——而它们入库之后 mergeInto "
        "会失败，Deploy 与 Preview 双双 500，人在界面上找不到入口删它。"
        "PutDraft 早就在入口挡了（issue #58），PutDrafts 是同一张表的另一个入口",
        "internal/store/drafts.go",
        "\t\tif _, err := asObject(patch); err != nil {\n\t\t\treturn fmt.Errorf(\"写回草稿 %s: %w\", resKey, err)\n\t\t}",
        "\t\t_ = patch\n\t\t_ = resKey",
        "./internal/store/", "TestPutDraftsRejectsNonObjects",
        "整批应当被拒",
    ),
    Probe(
        "改服务商-轮换空了不算失败",
        "说成「同步到服务商失败」会把人送去查凭证、查网络、翻服务商状态页，"
        "而那边什么毛病也没有——要做的是把节点的解析开回来。#36 给它分了"
        "单独的错误类型正是为了这个（issue #81）",
        "internal/api/settings.go",
        '\t\treturn false, "服务商设置已保存，而当前解析轮换里没有任何节点，解析未变动"',
        '\t\treturn false, "服务商设置已保存，但同步到服务商失败：" + err.Error()',
        "./internal/api/", "TestProviderSyncTellsEmptyRotationApartFromAFailure",
        "不该出现",
    ),
    Probe(
        "下发-断连不掐半途",
        "applyCtx 跟着隧道走的话，断连会把下发停在 SetRules 与 writeUpstreamCert "
        "之后、ApplyConfig 之前——新校验规则配旧 Caddy 配置，而 cfg_version 还是"
        "旧值，于是重连后主控看到的是「这台没跟上」，不是「这台状态不自洽」",
        "internal/agent/agent.go",
        "context.WithTimeout(context.WithoutCancel(ctx), deadline)",
        "context.WithTimeout(ctx, deadline)",
        "./internal/agent/", "TestApplyFinishesEvenIfTheTunnelDrops",
        "想要 cfg-1",
    ),
    Probe(
        "serve-等它起的下发跑完",
        "wg 漏掉 handlePush 的话，serve 返回时那份下发还在灌配置，而 main 立刻"
        "重连——两次连接的两份配置同时在应用，最终态取决于谁后到，两份回执都说成功",
        "internal/agent/agent.go",
        "\t\t\twg.Add(1)\n\t\t\tgo func() { defer wg.Done(); a.handlePush(connCtx, out, m.Push) }()",
        "\t\t\tgo a.handlePush(connCtx, out, m.Push)",
        "./internal/agent/", "TestServeWaitsForThePushItStarted",
        "serve 就返回了",
    ),
    Probe(
        "补推-册子上记的是哪一个",
        "清理时只判「有没有」不判「是不是自己」的话，老任务退出会把新任务的 "
        "cancel 划掉，此后 CancelAll 扫不到它。一次迟到的补推就能把旧配置推给"
        "已经拿到新版的节点（issue #60）",
        "internal/deploy/retry.go",
        "if cur := r.running[job.deployID]; cur != nil && cur.id == me {",
        "if cur := r.running[job.deployID]; cur != nil {",
        "./internal/deploy/", "TestCancelAllStopsTheJobThatIsActuallyRunning",
        "CancelAll 之后又推了",
    ),
    Probe(
        "告警-只重试时间修得好的",
        "对任何非 2xx 都重试的话，一个配错的 Webhook 每条告警打三遍。"
        "404 / 401 / 400 说的是「地址写错了 / 凭证不对 / 载荷被拒」——"
        "再发两遍只是把同一句拒绝再听两遍（issue #56）",
        "internal/alert/alert.go",
        "\t\tif !worthRetrying(resp.StatusCode) {\n\t\t\treturn last\n\t\t}",
        "",
        "./internal/alert/", "TestOnlyRetryWhatTimeCanFix",
        "打了 3 次，想要 1",
    ),
    Probe(
        "解析同步-同一批离线只推一次",
        "Detach/Attach 都是「按库里的现状推一遍」，与是哪个节点触发的无关。"
        "6 台同时掉线就推 6 次内容完全相同的全量同步，而服务商侧既没有退避"
        "也没有调用上限（issue #55）",
        "internal/dnsops/orchestrator.go",
        "\treturn o.syncCoalesced(ctx)\n}\n\nfunc (o *Orchestrator) Attach",
        "\treturn o.Sync(ctx, nil)\n}\n\nfunc (o *Orchestrator) Attach",
        "./internal/dnsops/", "TestConcurrentDetachesCollapseIntoOneSync",
        "推了 6 次全量同步",
    ),
    Probe(
        "主控-收到信号走关停路径",
        "gin 的 Run 是 ListenAndServe，没有关停钩子。原先出错直接 os.Exit(1)，"
        "于是 defer st.Close() / tun.Stop() 一次也不会执行，而重启是例行操作"
        "（issue #65）",
        "cmd/master/serve.go",
        "\t\tif onShutdown != nil {",
        "\t\tif false {",
        "./cmd/master/", "TestServeShutsDownGracefully",
        "关停动作跑了",
    ),
    Probe(
        "CF-中途失败说清做到哪一步",
        "只回叶子错误的话，账号里此刻是「cn 的 pool 是新的、load balancer 还指着"
        "旧 pool」，而同步状态只说「失败」——人不知道该去收拾什么。"
        "「什么都没做就失败」和「做了一半」要人做的事完全不同（issue #57）",
        "internal/dnsctl/cloudflare.go",
        "\t\t\treturn fmt.Errorf(\"%s：已更新 %s，load balancer 未改动（仍指向旧 pool）：%w\",\n\t\t\t\tname, doneSoFar(poolIDs), err)",
        "\t\t\treturn err",
        "./internal/dnsctl/", "TestCloudflareLBFailureSaysHowFarItGot",
        "报错没说做到哪一步了",
    ),
    Probe(
        "存在性检查-报错说出哪一步失败",
        "只判 ErrNotFound、其余 err 往下走的话，数据库抖一下「这条路由存在」"
        "这个前提就没被验证过。走下去那次写入同样会失败，但那个错说的是"
        "「修改路由失败」，而真正倒下的是它前面那次读（issue #66）",
        "internal/api/config_res.go",
        "\tcase err == nil:\n\t\treturn true",
        "\tcase !errors.Is(err, store.ErrNotFound):\n\t\treturn true",
        "./internal/api/", "TestExistenceChecksSayWhichStepFailed",
        "那是下一步的措辞",
    ),
    Probe(
        "告警-warn 档有滞回",
        "进出用同一个阈值的话，「在阈值上抖」会让状态反复翻转——而那是负载略高于"
        "阈值的机器的常态。每分钟最多 20 条事件 + 20 条 Lark，而一条天天亮着的"
        "告警，人两天就学会忽略它（issue #47）",
        "internal/health/health.go",
        "\tif prev == \"warn\" {",
        "\tif false {",
        "./internal/health/", "TestWarnDoesNotStormWhenLoadHoversOnTheThreshold",
        "发了 10 条告警",
    ),
    Probe(
        "流量-样本要是当下的",
        "只看条目在不在的话，一台掉线但还没被删除的节点会带着一小时前的数字"
        "继续算进 reported，「报数不齐就不记」那道闸因此被绕过去——"
        "而 24 小时后那个偏低的样本会成为同比的分母（issue #48）",
        "internal/traffic/traffic.go",
        "\t\tif time.Since(m.At) > staleAfter {\n\t\t\tcontinue\n\t\t}",
        "",
        "./internal/traffic/", "TestStaleSamplesDoNotCountAsReported",
        "「报数不齐就不记」那道闸会被一份冻结的样本绕过去",
    ),
    Probe(
        "设置-cert-bot 单独报",
        "只回 ops_bot_token_configured 的话，证书页只能拿它当「推送链路通不通」"
        "的指示灯：配对了 cert-bot 的人看到「未配置」，而配了 ops-bot 的人拿到"
        "绿灯——同时把整个控制面交给了外部平台（issue #51）",
        "internal/api/settings.go",
        "\t\t\"cert_bot_token_configured\": s.certBotConfigured,",
        "",
        "./internal/api/", "TestSettingsReportsCertBotSeparately",
        "没有 cert_bot_token_configured",
    ),
    Probe(
        "规则-新建不覆盖已有的",
        "PUT 是 upsert，而前端的重名保护是本地那份可能为空或陈旧的列表。"
        "后端不拒的话，它把已有那条整个换掉还回 code: 0——静默覆盖别人配好的"
        "规则，两边都没有提示（issue #70）",
        "internal/api/config_res.go",
        "\tonlyCreate := c.GetHeader(\"If-None-Match\") == \"*\"",
        "\tonlyCreate := false",
        "./internal/api/", "TestPutRuleRefusesToOverwriteWhenAskedNotTo",
        "原规则被覆盖了",
    ),
    Probe(
        "权重-撤空轮换也存得下来",
        "「全部退出轮换」是人可能真的想做的事（一次计划内的全网维护）。"
        "先推后存、推不成就地 return 的话，这个意图既存不下来也没人告诉他"
        "该怎么办，而同一个 handler 对「没配服务商」的处置是相反的（issue #81）",
        "internal/api/dns.go",
        "\tcase errors.As(err, &emptyRotation):",
        "\tcase false:",
        "./internal/e2e/", "TestAllZeroWeightsAreStillSaved",
        "却被拒了",
    ),
    Probe(
        "边缘端口-IPv6 也解析得出来",
        "按第一个冒号切的话，[::1]:443 得到 port=\":1]:443\"，ParseUint 失败被静默"
        "丢弃——那个端口上的连接从此不被统计，而症状是「一台正在扛流量的机器报 0」"
        "（issue #75）",
        "internal/agent/agent.go",
        "\t\t\t_, port, err := net.SplitHostPort(l)",
        "\t\t\tif len(l) > 0 && l[0] == '[' {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\t_, port, err := net.SplitHostPort(l)",
        "./internal/agent/", "TestEdgePortsHandlesIPv6AndBareColon",
        "丢掉的那些端口上的连接不会被统计",
    ),
    Probe(
        "校验端点-等价写法互相认",
        "normalizeAddr 只是 TrimSpace 的话，EC_VERIFY_LISTEN 写成 :2020 会被整份"
        "拒绝下发，并给出一条「请让两边一致」的错误——而人已经认为它们一致了"
        "（issue #63）",
        "internal/agent/verify.go",
        "\tcase \"\", \"0.0.0.0\", \"::\", \"[::]\", \"localhost\":\n\t\thost = \"127.0.0.1\"",
        "\tcase \"__never__\":\n\t\thost = \"127.0.0.1\"",
        "./internal/agent/", "TestVerifyAddrAcceptsEquivalentSpellings",
        "指向同一个端点，却被拒了",
    ),
    Probe(
        "重放缓存-热路径不扫表",
        "admit 顺手扫全表的话，高 QPS 的受保护域名上每个请求都是一次 O(n)，"
        "而且握着锁。表的规模是「窗口内的合法签名数」（issue #74）",
        "internal/agent/verify.go",
        "\tc.scans++ // 只为测试能问「刚才扫了几条」，生产路径上没人读它",
        "\tfor k, dies := range c.seen {\n\t\tc.scans++\n\t\tif now.After(dies) {\n\t\t\tdelete(c.seen, k)\n\t\t}\n\t}",
        "./internal/agent/", "TestAdmitDoesNotScanTheWholeTable",
        "这是每请求一次的 O(n)",
    ),
    Probe(
        "告警-成败是布尔不是文案",
        "把布尔拼进中文再用 Contains 搜回来的话，改一次文案它就静默失效，"
        "而失效的样子是审计里全绿（issue #77）",
        "internal/alert/alert.go",
        "\t\tif !r.ok {",
        "\t\tif strings.Contains(r.detail, \"失败\") {",
        "./internal/alert/", "TestDeliveryResultDoesNotComeFromWording",
        "记成了",
    ),
    Probe(
        "下发-一次只跑一次",
        "两次下发同时往节点上写的话，每台机器的终态取决于它自己那一侧谁后到，"
        "而两次下发的记录都会说成功。CancelAll 挡不住它——那一条管的是补推，"
        "首轮推送不经过重试器（issue #32）",
        "internal/deploy/deploy.go",
        "\ts.deployMu.Lock()\n\tdefer s.deployMu.Unlock()",
        "",
        "./internal/deploy/", "TestConcurrentDeploysDoNotInterleave",
        "的推送在时间上重叠了",
    ),
    Probe(
        "登录-有上限，不只有 bcrypt",
        "bcrypt 是一道按 CPU 计价的防线：它让每次尝试变慢，但没有上限。"
        "而控制台的「只绑内网」是部署形态，代码里没有任何东西检查它"
        "（ADR-0013 自己写着）——绑成 0.0.0.0 的主控上，这就是公网上一个"
        "没有速率上限的口令接口（issue #33）",
        "internal/api/auth.go",
        "\tif s.logins.blocked(ipKey, userKey) {",
        "\tif false {",
        "./internal/api/", "TestLoginIsRateLimited",
        "都没有被拦",
    ),
    Probe(
        "GeoIP-超限报错不截断",
        "ReadAll(LimitReader(...)) 超限时返回前 N 个字节且 err 为 nil，"
        "于是被剪掉尾巴的库当成正常库落盘——而加载时报的是一个格式错误，"
        "人会去查下载源而不是想到限额（issue #35）",
        "internal/geoip/fetch.go",
        "\tif int64(len(b)) > max {",
        "\tif false {",
        "./internal/geoip/", "TestOversizedDBIsRejectedNotTruncated",
        "超限的库应当被拒",
    ),
    Probe(
        "保留期-过期会话清掉、没过期的留着",
        "三张只写不清的表原先没有任何删除路径。sessions 的判据与另两张不同："
        "它删的是**已经过期的**，而不是「够老的」——删错方向的话，"
        "人会在使用中途被踢出去（issue #34）",
        "internal/store/retention.go",
        "`DELETE FROM sessions WHERE expires_at <= now()`",
        "`DELETE FROM sessions WHERE expires_at > now()`",
        "./internal/store/", "TestPruneKeepsWhatIsStillUseful",
        "没过期的会话被清掉了",
    ),
    Probe(
        "主控-端口起不来要报错不要挂住",
        "关停 goroutine 只等 ctx.Done()，而主流程无条件等它——bind 阶段就失败时"
        "serve 永远不返回：主控既不退出也不报错，systemd 看到一个活着的进程。"
        "原先那条 srv.Run 至少会 os.Exit(1)（#65 的改造引入）",
        "cmd/master/serve.go",
        "\t\tcase <-stopped:\n\t\t\t// 服务已经自己退了，没有什么要优雅关停的。\n\t\t\treturn",
        "\t\tcase <-make(chan struct{}):\n\t\t\treturn",
        "./cmd/master/", "TestServeReturnsWhenItCannotListen",
        "serve 没有返回",
    ),
    Probe(
        "回源数-不下溢",
        "origin 来自 Caddy（重启即归零），denied 是 Agent 进程内的累计值"
        "（不重启就不归零）。节点上重启一次 Caddy，uint64 减法绕成 1.8e19，"
        "而这个数会进 traffic_samples 并在 24 小时后当同比的分母（#41 的改造引入）",
        "internal/agent/metrics.go",
        "\tif a < b {\n\t\treturn 0\n\t}",
        "\tif false {\n\t\treturn 0\n\t}",
        "./internal/agent/", "TestOriginNeverUnderflows",
        "uint64 减法下溢了",
    ),
    Probe(
        "续期-与下发同一条队",
        "续期循环也是整份渲染、逐节点推——与一次下发在节点上没有区别。"
        "不拿 deployMu 的话 #32 的竞态原样成立，只是发起方换成了定时器"
        "（#39 新开的推送路径，修 #32 时没覆盖到）",
        "internal/deploy/renew.go",
        "\ts.deployMu.Lock()\n\tdefer s.deployMu.Unlock()\n",
        "",
        "./internal/deploy/", "TestRenewalDoesNotInterleaveWithADeploy",
        "的推送在时间上重叠了",
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


def run_probes(probes):
    """跑一批探针，返回 [(probe, verdict, detail)] 与被碰过的文件快照。"""
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
    return results, touched


def restore_check(touched):
    """收尾自检：源码必须还原成探针找到它时的样子。

    **不用 git。** `git diff` 问的是「相对 HEAD 脏不脏」，而这里要问的是
    「探针有没有还原它自己的改动」——文件本来就有未提交改动时两者分岔，
    那时 git 会冤枉一个干得很好的探针。拿了个相邻问题的答案，
    就会在边缘情况上得到错的结论，而边缘情况正是你需要它的时候。
    """
    return [str(path.relative_to(ROOT)) for path, orig in touched
            if path.read_text(encoding="utf-8") != orig]


def self_test():
    """**探针脚本自己也要被检查。**

    这三种失败是我在写这个脚本的过程中真实撞上的，不是设想出来的：
    第四问那次，我的临时改动因为一个 TypeError 根本没落进文件，
    而我看到「11/11 通过」就往下走了。

    手工跑一遍自检然后忘掉，跟固化探针之前的状态一模一样 —— 所以固化它。

    这一层封顶是合理的：产品代码的失败模式无穷，而这个脚本只有四问、
    每问只有一种失败方式，可以枚举完。
    """
    real = PROBES[2]  # 心跳那条：跑得快，且不需要 Caddy
    cases = [
        ("改坏是无害的", Probe(real.name, real.why, real.file, real.old, real.old,
                          real.pkg, real.test, real.expect_line), "没红"),
        ("期望片段对不上", Probe(real.name, real.why, real.file, real.old, real.new,
                           real.pkg, real.test, "某句永远不会出现的话"), "红在别处"),
        ("要替换的片段不存在", Probe(real.name, real.why, real.file, "这段代码不存在",
                             real.new, real.pkg, real.test, real.expect_line),
         "改坏没匹配到"),
        ("目标测试名写错", Probe(real.name, real.why, real.file, real.old, real.new,
                          real.pkg, real.test + "TYPO", real.expect_line),
         "一条测试都没跑"),
    ]
    bad = 0
    for label, probe, want in cases:
        results, touched = run_probes([probe])
        got = results[0][1]
        mark = "✓" if got == want else "✗"
        if got != want:
            bad += 1
        print(f"  {mark} {label} → 应报「{want}」，实际「{got}」")
        if dirty := restore_check(touched):
            print(f"     ✗ 而且没还原干净：{dirty}")
            bad += 1

    # 第五种：还原自身失效。这一条只能直接测 restore_check ——
    # 它是唯一一个「探针跑完之后」才能问的问题。
    probe_file = ROOT / "scripts" / "probes.py"
    fake_touched = [(probe_file, probe_file.read_text(encoding="utf-8") + "\n# 假的原始内容")]
    if restore_check(fake_touched) != ["scripts/probes.py"]:
        print("  ✗ 还原自检认不出被改过的文件")
        bad += 1
    else:
        print("  ✓ 还原自检认得出被改过的文件")

    print(f"\n  自检 {5 - bad}/5 通过")
    return 1 if bad else 0


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--self-test":
        return self_test()
    only = sys.argv[1] if len(sys.argv) > 1 else ""
    probes = [p for p in PROBES if only in p.name]
    if not probes:
        print(f"没有名字含 {only!r} 的探针")
        return 1

    results, touched = run_probes(probes)
    ok = sum(1 for _, verdict, _ in results if verdict == "ok")
    for p, verdict, detail in results:
        mark = "✓" if verdict == "ok" else "✗"
        print(f"  {mark} {p.name}" + ("" if verdict == "ok" else f"  —— {verdict}"))
        if verdict != "ok":
            print(f"     {detail}")
            print(f"     它保护的是：{p.why}")

    # 收尾自检：源码必须还原成探针找到它时的样子。
    #
    # **一个会留下损伤的探针比没有探针更糟**——它不只是自己不可信，
    # 它让后面所有测试的结论也变得不可信，因为那些测试跑的不是真正的源码。
    dirty = restore_check(touched)
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
