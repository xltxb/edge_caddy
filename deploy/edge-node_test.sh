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

printf '\n──────────\n通过 %d，失败 %d\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
