#!/usr/bin/env bash
#
# 部署脚本的测试。
#
# 它跑在开发机上（macOS 也行），只验**决策逻辑**：发行版探测、监听地址判定、
# 单元文件与防火墙规则的生成。这些是脚本里真正会写错、又不需要 Linux 的部分。
#
# **「装上去能起来」这件事这里验不了**，需要一台真机。不假装验过——
# 一份「全部通过」而其中几条其实没验的报告，比没有这份报告更糟。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./edge-node.sh
source "$HERE/edge-node.sh"
# edge-node.sh 顶上有 set -e（脚本自己需要）。测试要故意走失败路径，
# 因此 source 之后立刻关掉——否则第一条「本该失败」的断言会把整个测试中断。
set +e

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  ✘ %s\n' "$1"; [ $# -gt 1 ] && printf '      %s\n' "$2"; return 0; }
eq()   { [ "$1" = "$2" ] && ok "$3" || bad "$3" "期望 [$1]，实际 [$2]"; }
has()  { case "$1" in *"$2"*) ok "$3" ;; *) bad "$3" "找不到 [$2]" ;; esac; }
hasnt(){ case "$1" in *"$2"*) bad "$3" "不该出现 [$2]" ;; *) ok "$3" ;; esac; }
# no_directive 断言某条 systemd 指令**没有**出现。
#
# 不能用 hasnt：单元文件里有一句解释「为什么不用 Requires=caddy.service」的注释，
# 而整段搜字符串分不出「指令」和「关于该指令的注释」——那会把说明看成违规。
# 只匹配行首（允许前导空白）的指令。
no_directive(){
  if printf '%s\n' "$1" | grep -qE "^[[:space:]]*$2"; then
    bad "$3" "单元里出现了指令 [$2]"
  else
    ok "$3"
  fi
}
# has_line 断言**整行**就是这个，后面不许有别的。
#
# 用 has 的话，`CapabilityBoundingSet=` 会被
# `CapabilityBoundingSet=CAP_NET_BIND_SERVICE` 满足——而那两者的含义
# 正好相反：一个是「一个都不给」，一个是「给这一个」。
has_line(){
  if printf '%s\n' "$1" | grep -qE "^$2$"; then
    ok "$3"
  else
    bad "$3" "找不到整行 [$2]"
  fi
}

fixture() { printf '%s/testdata/%s' "$HERE" "$1"; }

printf '\n发行版探测\n'
eq debian "$(os_family "$(fixture os-release.debian12)")" "Debian 12"
eq debian "$(os_family "$(fixture os-release.ubuntu2204)")" "Ubuntu 22.04"
eq alpine "$(os_family "$(fixture os-release.alpine)")" "Alpine"
eq arch   "$(os_family "$(fixture os-release.arch)")" "Arch"
# Rocky / Alma 的 ID 认不出来，靠 ID_LIKE 里的 rhel。只看 ID 就要把每个
# RHEL 衍生版单独列一遍，而它们的包管理是一样的。
eq rhel   "$(os_family "$(fixture os-release.rocky9)")" "Rocky 9（走 ID_LIKE）"
eq rhel   "$(os_family "$(fixture os-release.alma9)")" "AlmaLinux 9（走 ID_LIKE）"

printf '\n认不出的发行版要失败，不能猜\n'
tmp="$(mktemp)"; printf 'ID=plan9\n' > "$tmp"
out="$(os_family "$tmp")"; rc=$?
eq unknown "$out" "认不出时返回 unknown"
[ "$rc" -ne 0 ] && ok "并且以非零退出" || bad "认不出时应当非零退出"
caddy_install_plan unknown >/dev/null 2>&1
[ $? -ne 0 ] && ok "安装计划对未知家族失败，不去猜一个 curl | bash" \
             || bad "未知家族不该给出安装计划"
rm -f "$tmp"

printf '\n监听地址判定（这是脚本里最要紧的一处）\n'
port_is_loopback_only "$(fixture ss-loopback-only.txt)" 2019 2>/dev/null
eq 0 "$?" "只在 127.0.0.1 上监听 → 通过"
port_is_loopback_only "$(fixture ss-v6-loopback.txt)" 2019 2>/dev/null
eq 0 "$?" "只在 ::1 上监听 → 通过"
port_is_loopback_only "$(fixture ss-admin-exposed.txt)" 2019 2>/dev/null
eq 1 "$?" "监听 0.0.0.0 → 拒绝"
port_is_loopback_only "$(fixture ss-admin-lan-ip.txt)" 2019 2>/dev/null
eq 1 "$?" "监听内网 IP → 拒绝（内网不等于回环）"
port_is_loopback_only "$(fixture ss-admin-wildcard-v6.txt)" 2019 2>/dev/null
eq 1 "$?" "监听 :: → 拒绝"
# 「没在监听」与「监听错地方」要分开：前者是 Caddy 没起来，后者是私钥暴露。
# 两种的处置完全不同，合成一个返回值就分不出来了。
port_is_loopback_only "$(fixture ss-not-listening.txt)" 2019 2>/dev/null
eq 2 "$?" "没在监听 → 与「暴露」区分开的另一个返回值"

printf '\n端口号不能被后缀匹配蒙混\n'
# :12019 结尾是 2019，但它不是 2019。夹具里没有这种情况，
# 所以自己造一个——这类错在真机上表现为「查过了，通过了」，而其实查的是别的端口。
tmp="$(mktemp)"
cat > "$tmp" <<'SS'
State  Recv-Q Send-Q Local Address:Port  Peer Address:Port
LISTEN 0      4096         0.0.0.0:12019       0.0.0.0:*
SS
port_is_loopback_only "$tmp" 2019 2>/dev/null
eq 2 "$?" "0.0.0.0:12019 不该被当成 2019"
rm -f "$tmp"

printf '\nsystemd 单元里那几条承重的约束\n'
unit="$(agent_unit)"
has "$unit" "Restart=always" "Restart=always（ADR-0003：fail-closed 依赖 Agent 存活）"
has "$unit" "EnvironmentFile=/etc/edge-agent.env" "凭据走 EnvironmentFile"
hasnt "$unit" "EC_ENROLL_TOKEN" "Token 不出现在单元里（ExecStart 的参数会进 ps）"
no_directive "$unit" "Requires=" "不 Requires caddy —— Caddy 挂了 Agent 要活着把这件事报上去"
has "$unit" "ProtectSystem=strict" "沙箱：节点被打穿后唯一还在的那道墙"
has "$unit" "ReadWritePaths=/var/lib/edge-agent" "状态目录可写"

printf '\nAgent 不以 root 跑\n'
#
# **root 身份下 ProtectSystem=strict 挡不住多少。**
#
# 盘过 Agent 的全部特权操作，一项都不需要 root：写文件只落在 StateDir
#（agent.go 的 certPath 与 StateDir、geoip.go 的 mmdb），校验端点监听
# 127.0.0.1:2020 是非特权端口，出站连主控与访问 Caddy Admin 都不要特权，
# 排空走 Caddy 的 metrics（drain.go 的 countConns）而不是 /proc。
#
# 回源证书路径是主控下发的（PushConfig.upstream_cert_path），看起来像个
# 任意文件写入口——但它的默认值就在 StateDir 里（config.go 的
# EC_UPSTREAM_CERT），而 ReadWritePaths 早已把它锁死在那儿了。
no_directive "$unit" "User=root" "不以 root 跑"
has "$unit" "User=edge-agent" "跑在专用系统用户下"
has "$unit" "Group=edge-agent" "同名的组"
# **空的 CapabilityBoundingSet 是这一组里最要紧的一条。**
#
# 非 root 进程本来就没有 capability，所以它现在是冗余的——它防的是**以后**：
# 哪天有人为了图省事把 User 改回 root，这一行让那件事不再等于「拿回全部特权」。
has_line "$unit" "CapabilityBoundingSet=" "capability 集清空"
has_line "$unit" "AmbientCapabilities=" "不主动获取任何 capability"

printf '\n沙箱里那些非 root 才谈得上的项\n'
for d in PrivateDevices=yes ProtectClock=yes ProtectHostname=yes \
         ProtectKernelLogs=yes RestrictSUIDSGID=yes RestrictNamespaces=yes \
         RestrictRealtime=yes SystemCallArchitectures=native UMask=0077; do
  has "$unit" "$d" "沙箱：$d"
done
has "$unit" "SystemCallFilter=@system-service" "系统调用走白名单"
# **MemoryDenyWriteExecute 是故意不加的。**
#
# 对 Go 程序通常没问题，但它属于「出问题时表现为进程在某次 GC 或某个
# cgo 调用上随机崩溃」的那一类——而 Agent 挂掉那一刻，受保护域名整体 502
#（ADR-0003 的 fail-closed）。这点收益配不上那个风险。
no_directive "$unit" "MemoryDenyWriteExecute" "不加 MemoryDenyWriteExecute（Go 上风险大于收益）"

printf '\n建专用系统用户\n'
has "$(agent_user_plan debian)" "useradd" "Debian 系用 useradd"
has "$(agent_user_plan rhel)" "useradd" "RHEL 系用 useradd"
has "$(agent_user_plan alpine)" "adduser" "Alpine 的 busybox 没有 useradd"
has "$(agent_user_plan debian)" "nologin" "不给登录 shell"
# 用同一条命令跑第二遍不能失败：重装、升级都会再跑一次 do_install，
# 而 useradd 撞上已存在的用户是非零退出——那会让整个安装在这一步断掉。
has "$(agent_user_plan debian)" "id -u edge-agent" "先判存在，重跑不炸"
# 认不出的家族要失败，跟 caddy_install_plan 一致。猜一条建用户命令的后果是
# 「用户没建成、Agent 起不来」，而 systemd 报的是 217/USER —— 那个错误码
# 不会有人联想到发行版探测。
agent_user_plan unknown >/dev/null 2>&1
[ $? -ne 0 ] && ok "未知家族不给建用户计划，跟安装 Caddy 一致" \
             || bad "未知家族不该给出建用户计划"

printf '\n降权之后那两个文件还得读得到\n'
#
# **这一组守的是降权最容易砸的地方。**
#
# /etc/edge-agent.env 现在是 0600 root:root。改完 User= 之后 Agent 读不到它，
# 而症状是进程起不来、报「缺少 EC_ENROLL_TOKEN」——看起来像 Token 没写进去，
# 人会去查接入流程，那儿没有问题。
script_text="$(cat "$HERE/edge-node.sh")"
has "$script_text" 'chown root:"$AGENT_USER" "$AGENT_ENV"' "ENV 文件归组给 edge-agent"
has "$script_text" 'chmod 0640 "$AGENT_ENV"' "ENV 文件 0640（组可读，其他人不可读）"
no_directive "$script_text" 'chmod 0600 "\$AGENT_ENV"' "不再是 0600（那样降权后读不到）"
# **存量节点的状态目录是 root:root 0700，里面躺着隧道客户端证书私钥。**
# StateDirectory= 对已存在的目录会不会重设属主，我不打算靠记忆断言——
# 显式 chown 一次，代价是零。
has "$script_text" 'chown -R "$AGENT_USER":"$AGENT_USER" "$AGENT_HOME"' \
  "状态目录改属主（存量节点是 root:root）"

printf '\nEnvironmentFile 的内容\n'
env_out="$(agent_env ec.internal:9000 node-hk-01 ec_tok 9e8f22a3)"
has "$env_out" "EC_ENROLL_TOKEN=ec_tok" "带一次性 Token"
has "$env_out" "EC_CA_PIN=9e8f22a3" "带 CA 指纹 —— 少了它接入退化成 TOFU"
has "$env_out" "EC_CADDY_ADMIN=http://127.0.0.1:2019" "Caddy Admin 指回环"
has "$env_out" "EC_VERIFY_LISTEN=127.0.0.1:2020" "校验端点绑回环"

printf '\n防火墙规则\n'
fw="$(firewall_plan debian no)"
has "$fw" "80/tcp" "放行 80"
has "$fw" "443/tcp" "放行 443"
hasnt "$fw" "443/udp" "没开 HTTP/3 时不放行 443/udp（白送一个攻击面）"
hasnt "$fw" "2019" "绝不放行 Caddy Admin"
hasnt "$fw" "2020" "绝不放行校验端点"
fw3="$(firewall_plan debian yes)"
has "$fw3" "443/udp" "开了 HTTP/3 才放行 443/udp"

printf '\nCaddy Admin 钉在回环\n'
has "$(caddy_admin_dropin)" "CADDY_ADMIN=127.0.0.1:2019" "drop-in 显式钉死 Admin 地址"

printf '\n装机前的连通性检查\n'
#
# 这一条守的是**「在动这台机器之前先停下」**。
#
# 少了它，装机会一路成功——装 Caddy、写单元、起 Agent——然后节点永远不
# 出现在控制台里，而这台机器上没有任何东西说得出为什么。灰度上真实撞到过：
# 主控的域名被 Cloudflare 代理，CDN 只转发 80/443，9000 根本到不了主控。
#
# **不能用「装完再看有没有上线」代替**：那时错误已经散落在三个地方
#（Agent 日志、systemd 状态、控制台的空列表），而没有一处说得出真因。

# 端口格式错要当场拒，而不是拿一个空 port 去连。
out="$( (preflight_master "ec.example.com") 2>&1 )"
has "$out" "host:port" "--master 没写端口时点名格式"

# 连不上时必须**拒绝继续**，并且报错里要给出可查的方向。
# 用 127.0.0.1 上一个几乎不可能有人监听的端口。
out="$( (preflight_master "127.0.0.1:59321") 2>&1 )"
has "$out" "连不上" "连不上时拒绝安装"
has "$out" "CDN" "报错点出「域名被 CDN 代理」这个最常见的成因"
has "$out" "ss -lntp" "报错给出在主控那边怎么验"
has "$out" "nc -vz" "报错给出在节点这边怎么验"

# **反向：连得上就不该拦。** 没有这一条，上面四条在
#「preflight 无条件失败」时也全绿——而那会让每一次安装都装不了。
python3 -c 'import socket,sys,time,threading
s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1)
print(s.getsockname()[1]); sys.stdout.flush()
time.sleep(6)' > /tmp/ec-preflight-port.txt &
sleep 1
listen_port="$(head -1 /tmp/ec-preflight-port.txt 2>/dev/null)"
if [ -n "$listen_port" ]; then
  out="$( (preflight_master "127.0.0.1:$listen_port") 2>&1 )"
  hasnt "$out" "连不上" "连得上时放行（否则上面几条在「无条件失败」时也全绿）"
else
  bad "连得上时放行" "装置坏了：没能起一个临时监听端口"
fi
wait 2>/dev/null

# **wss:// 那种写法也要能解析出主机和端口。**
#
# 用 ${addr%:*} 那一套的话，`wss://cdn.example.com` 会被切成 host="wss"，
# 然后拿着 "wss" 去连 —— 报的是「域名解析失败」，而人会去查 DNS，
# 那儿没有问题。
out="$( (preflight_master "wss://127.0.0.1:59322") 2>&1 )"
has "$out" "127.0.0.1:59322" "wss://host:port 解析出主机和端口"
hasnt "$out" "wss:" "主机名里不该混进协议头"

# 不带端口时补默认值：wss → 443，ws → 80。
out="$( (preflight_master "wss://cdn.invalid.example") 2>&1 )"
has "$out" "cdn.invalid.example:443" "wss:// 不带端口时补 443"

# 带路径也要能剥掉（安装命令里可能带 /api/v1/tunnel）。
out="$( (preflight_master "wss://cdn.invalid.example/api/v1/tunnel") 2>&1 )"
has "$out" "cdn.invalid.example:443" "wss:// 带路径时只取主机和端口"
hasnt "$out" "/api" "路径不该混进主机名"

# 别的协议要当场拒，而不是猜。
out="$( (preflight_master "https://cdn.example.com") 2>&1 )"
has "$out" "只能是 wss" "https:// 被明确拒绝，而不是悄悄当成 wss"

printf '\nverify 自己不能崩\n'
#
# 灰度上真发生过：六条检查全部 ✓ 打印完，然后
#
#   ./edge-node.sh: line 1: ss_out: unbound variable
#
# 成因是 `trap 'rm -f "$ss_out"' EXIT` —— **单引号让 $ss_out 留到退出那一刻
# 才求值**，而它是 local 的，函数一返回就出了作用域。
#
# 后果不只是难看：**退出码跟着变成非零**，任何按 verify 的结果判断的地方
# 都会把一台完全健康的节点当成失败。
#
# 这条测试在开发机上跑，那儿没有 systemctl 也没有 ss，所以 do_verify 的
# 每一项都会判失败 —— **无所谓**。它验的不是那些项的结论，
# 是「这个函数从头跑到尾、退出时不炸」。
out="$( (do_verify) 2>&1 )"
hasnt "$out" "unbound variable" "verify 跑完不留 unbound variable"
# 原先这里还有一条 `hasnt "$out" "line 1:"`。删了：macOS 的 bash 报的是
# `bash: ss_out: unbound variable`，不带 "line 1:" —— **那条断言在这台机器上
# 永远不会红**。一条不会红的断言不是多一层保障，是伪装成覆盖的噪音。
# 反向：确认它**真的跑到了最后**。没有这一条，上面两条在
# 「do_verify 第一行就返回」时也全绿 —— 那时它当然不会有 unbound variable。
has "$out" "没有**查" "verify 跑到了最后那段说明（否则上面两条是空转）"

# ── update ────────────────────────────────────────────────────────────
#
# **这一组的存在理由是一次实测**：旧 agent 遇到新规则类型（rate_limit /
# geo_block）时，那个域名的**第一个请求就是 403** —— 不是降级，是整站关闭，
# 而配置看起来完全正常。所以「怎么更新 agent」不是运维细节，是安全功能的一部分。

usage_out="$(usage 2>&1)"
has "$usage_out" "update --agent-bin" "用法里有 update"
has "$usage_out" "自动回滚" "用法里说清了会自动回滚"
hasnt "$usage_out" "update --agent-bin <新二进制的路径> --token" \
  "update 不要 Token（这台机器已经有隧道证书了）"

# **同一个文件不算更新。**
#
# 拿同一个文件当「新版」是最常见的一种「更新了但什么也没变」——
# 而它之后的一切看起来都正常：进程重启了、日志也正常、版本号还是旧的。
tmpbin="$(mktemp)"; printf 'x' > "$tmpbin"
# 这里不真的跑 do_update（它要 systemctl），只验那句 cmp 的判据在脚本里。
has "$(cat "$HERE/edge-node.sh")" 'cmp -s "$agent_src" "$AGENT_BIN"' \
  "update 会先比对新旧是不是同一个文件"
has "$(cat "$HERE/edge-node.sh")" 'cp -p "$AGENT_BIN" "$backup"' \
  "update 换之前先备份"
has "$(cat "$HERE/edge-node.sh")" 'install -m 0755 "$backup" "$AGENT_BIN"' \
  "连不上主控时会用备份换回去"

# **等的是「隧道连上」，不是「进程还在」。**
#
# 进程活着而隧道连不上时，节点在控制台上是离线的 —— 而那正是更新最容易出的
# 那种问题。只判 is-active 的话，一次失败的更新会被报成成功。
has "$(cat "$HERE/edge-node.sh")" '接入完成' "update 等的是隧道真的连上了"
rm -f "$tmpbin"

# ── harden ────────────────────────────────────────────────────────────
#
# **这一组的存在理由**：install 要 --token，而 Token 是一次性的、存量节点
# 早就用掉了；update 只换二进制、不碰单元文件。没有 harden 的话，降权这件事
# 对所有已经在跑的节点是死的 —— 而没有任何地方会说出来。

printf '\nharden：把存量节点降权\n'
has "$usage_out" "harden" "用法里有 harden"
hasnt "$usage_out" "harden --token" "harden 不要 Token（Token 是一次性的，早用掉了）"
has "$usage_out" "502" "用法说清了会重启 Agent、受保护域名会短暂 502"
has "$script_text" 'harden)    shift; do_harden ;;' "harden 接进了子命令分发"

# **降权起不来那一刻，受保护域名整体 502。** 这时候没有时间去手写一份单元
# 回去，所以备份和自动回滚都不是可选项。
has "$script_text" 'cp -p "$AGENT_UNIT" "$backup"' "harden 重写单元前先备份"
has "$script_text" 'install -m 0644 "$backup" "$AGENT_UNIT"' "连不上主控时把单元换回去"

# **等的判据必须与 update 共用一份。**
# 分成两份的话，迟早有一份会停在「进程还在」上——而进程活着、隧道连不上
# 正是降权最可能出的那种故障（读不到 EnvironmentFile）。
eq 1 "$(grep -c '^wait_tunnel_up()' "$HERE/edge-node.sh")" "等隧道的判据只有一份"
eq 2 "$(grep -c 'if wait_tunnel_up; then' "$HERE/edge-node.sh")" "update 与 harden 都用它"

printf '\n──────────\n通过 %d，失败 %d\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
