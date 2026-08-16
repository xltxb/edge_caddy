#!/usr/bin/env bash
# 部署脚本的测试。
#
# 它跑在开发机上（macOS 也行），只验证**决策逻辑**：发行版探测、单元文件生成、
# 监听地址判定、防火墙规则生成。这些是脚本里真正会出错的地方，且不需要 Linux。
#
# 真正的「装上去能起来」验不了——那需要一台真机，见 deploy/README.md。
# 这里不假装验过。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./edge-node.sh
source "$HERE/edge-node.sh"
# edge-node.sh 顶上有 set -e（脚本自己需要它）。测试要故意触发失败路径，
# 因此 source 之后立刻关掉——否则第一条「本该失败」的断言会把整个测试中断。
set +e

PASS=0
FAIL=0

fail() {
  FAIL=$((FAIL + 1))
  printf '  ✘ %s\n' "$1"
  [ $# -gt 1 ] && printf '    %s\n' "$2"
  return 0
}

ok() {
  PASS=$((PASS + 1))
  printf '  ✓ %s\n' "$1"
}

assert_eq() { # 期望 实际 说明
  if [ "$1" = "$2" ]; then ok "$3"; else fail "$3" "期望 [$1]，实际 [$2]"; fi
}

assert_contains() { # 干草堆 针 说明
  case "$1" in
    *"$2"*) ok "$3" ;;
    *) fail "$3" "输出里找不到 [$2]" ;;
  esac
}

assert_not_contains() { # 干草堆 针 说明
  case "$1" in
    *"$2"*) fail "$3" "输出里不该出现 [$2]" ;;
    *) ok "$3" ;;
  esac
}

assert_fails() { # 说明 命令...
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then fail "$desc" "命令本该失败却成功了"; else ok "$desc"; fi
}

assert_succeeds() { # 说明 命令...
  local desc="$1"; shift
  local out
  if out="$("$@" 2>&1)"; then ok "$desc"; else fail "$desc" "$out"; fi
}

section() { printf '\n%s\n' "$1"; }

# ── 发行版探测 ──
#
# 认错发行版会用错的包管理器跑到一半失败，留下半装状态：Caddy 装了、
# Agent 没装、防火墙规则加了一半。所以不认识的必须当场拒绝，不能猜。

section "发行版探测"

assert_eq "debian" "$(detect_family "$HERE/testdata/os-release.debian12")" "Debian 12 → debian"
assert_eq "debian" "$(detect_family "$HERE/testdata/os-release.ubuntu2204")" "Ubuntu 22.04 → debian（靠 ID_LIKE）"
assert_eq "rhel" "$(detect_family "$HERE/testdata/os-release.rocky9")" "Rocky 9 → rhel"
assert_eq "rhel" "$(detect_family "$HERE/testdata/os-release.alma9")" "AlmaLinux 9 → rhel"

# Alpine 用 OpenRC，整套加固都建立在 systemd 上——装到一半才发现没有 systemctl
# 是最糟的失败方式
assert_fails "Alpine 被拒绝（没有 systemd）" detect_family "$HERE/testdata/os-release.alpine"
assert_fails "Arch 被拒绝（没有官方 Caddy 源）" detect_family "$HERE/testdata/os-release.arch"
assert_fails "读不到 os-release 时报错" detect_family "$HERE/testdata/does-not-exist"

# ── systemd 单元生成 ──
#
# 这里验的每一条都是承重的，不是风格问题：
#   - 凭据进了 ExecStart 就等于公开（ps 谁都能看）
#   - Agent 不自动重启，受保护域名的 fail-closed 就在它挂掉那一刻失效（ADR-0003）
#   - 沙箱是节点被打穿之后唯一还在的那道墙

section "Agent 的 systemd 单元"

AGENT_UNIT="$(agent_unit node-hk-01 master.example.com:9000)"

assert_contains "$AGENT_UNIT" "EnvironmentFile=" "凭据走 EnvironmentFile"
assert_not_contains "$AGENT_UNIT" "EDGE_ENROLL_TOKEN=" "Token 不出现在单元文件里"
assert_contains "$AGENT_UNIT" "Restart=always" "异常退出后自动重启"
assert_contains "$AGENT_UNIT" "RestartSec=" "重启有间隔，不打满 CPU"
assert_contains "$AGENT_UNIT" "--node-id node-hk-01" "带上节点 ID"
assert_contains "$AGENT_UNIT" "--master master.example.com:9000" "带上主控地址"
# 校验端点必须显式写出来并且是回环：Agent 那边默认值对了，但单元文件里写死
# 才能保证「面板上看到的」和「机器上跑的」是一回事
assert_contains "$AGENT_UNIT" "--verify-addr 127.0.0.1:${VERIFY_PORT}" "校验端点显式绑回环"

for opt in NoNewPrivileges=yes PrivateTmp=yes ProtectSystem=strict ProtectHome=yes \
           ProtectKernelTunables=yes ProtectKernelModules=yes ProtectControlGroups=yes \
           RestrictAddressFamilies= RestrictSUIDSGID=yes LockPersonality=yes \
           MemoryDenyWriteExecute=yes SystemCallArchitectures=native; do
  assert_contains "$AGENT_UNIT" "$opt" "Agent 沙箱：$opt"
done

# ProtectSystem=strict 之下整个文件系统只读，证书目录必须显式放开，
# 否则接入时写证书会失败——而失败信息是 EROFS，看着像磁盘坏了
assert_contains "$AGENT_UNIT" "ReadWritePaths=${EDGE_STATE_DIR}" "证书目录显式可写"

section "Caddy 的 systemd 覆盖"

CADDY_UNIT="$(caddy_override)"

# Caddy 要绑 80/443，只保留这一项能力；其余全部丢掉
assert_contains "$CADDY_UNIT" "AmbientCapabilities=CAP_NET_BIND_SERVICE" "保留绑定特权端口的能力"
assert_contains "$CADDY_UNIT" "CapabilityBoundingSet=CAP_NET_BIND_SERVICE" "能力上界只留这一项"
assert_contains "$CADDY_UNIT" "NoNewPrivileges=yes" "Caddy 沙箱：NoNewPrivileges"
assert_contains "$CADDY_UNIT" "PrivateTmp=yes" "Caddy 沙箱：PrivateTmp"
assert_contains "$CADDY_UNIT" "ProtectHome=yes" "Caddy 沙箱：ProtectHome"
# 官方包的单元里 ExecStart 用 Caddyfile；我们不改它，只加覆盖——
# 配置由主控经 Admin API 下发，覆盖文件里不该出现任何配置内容
assert_not_contains "$CADDY_UNIT" "ExecStart=" "覆盖文件不动 ExecStart"

section "接入凭据文件"

ENV_FILE="$(agent_env_file 'tok-abc123')"
assert_contains "$ENV_FILE" "EDGE_ENROLL_TOKEN=tok-abc123" "Token 写在 EnvironmentFile 里"

# ── 监听地址判定 ──
#
# Caddy Admin 没有任何鉴权，能访问的人等于拥有这台节点：可以改回源把流量引走、
# 读走全部配置。验证阶段必须**真查监听地址**，而不是相信配置文件里写了什么——
# 配置写对了但没重载、或者被别的配置覆盖，都会让文件与现实脱节。

section "监听地址判定"

assert_succeeds "只在 127.0.0.1 上 → 通过" \
  port_is_loopback_only "$HERE/testdata/ss-loopback-only.txt" 2019
assert_succeeds "只在 [::1] 上 → 通过" \
  port_is_loopback_only "$HERE/testdata/ss-v6-loopback.txt" 2019

assert_fails "绑在 0.0.0.0 上 → 拒绝" \
  port_is_loopback_only "$HERE/testdata/ss-admin-exposed.txt" 2019
assert_fails "绑在通配 * 上 → 拒绝" \
  port_is_loopback_only "$HERE/testdata/ss-admin-wildcard-v6.txt" 2019
assert_fails "绑在内网 IP 上 → 拒绝" \
  port_is_loopback_only "$HERE/testdata/ss-admin-lan-ip.txt" 2019

# 「没监听」不等于「安全」：Admin 没起来的话，下发会全部失败，
# 而验证阶段报个「通过」会让人以为一切正常
assert_fails "根本没在监听 → 也不算通过" \
  port_is_loopback_only "$HERE/testdata/ss-not-listening.txt" 2019

# 端口号不能靠子串匹配：2019 不该命中 12019 或 20190
assert_fails "端口号精确匹配，不被 20190 这类误判" \
  port_is_loopback_only "$HERE/testdata/ss-loopback-only.txt" 201

# 校验端点同样只能在回环上
assert_succeeds "校验端点在回环上 → 通过" \
  port_is_loopback_only "$HERE/testdata/ss-loopback-only.txt" 2021

# ── 防火墙规则 ──
#
# 防火墙是**第二道防线**：第一道是 Caddy Admin 只绑回环。两道都要有——
# 配置改错了还有防火墙兜着，防火墙被刷掉了还有绑定地址兜着。

section "防火墙规则"

UFW_PLAN="$(firewall_plan ufw)"
assert_contains "$UFW_PLAN" "ufw allow 80/tcp" "ufw 放行 80"
assert_contains "$UFW_PLAN" "ufw allow 443/tcp" "ufw 放行 443"
assert_contains "$UFW_PLAN" "ufw deny ${CADDY_ADMIN_PORT}/tcp" "ufw 拒绝 Admin 端口"
assert_contains "$UFW_PLAN" "ufw deny ${VERIFY_PORT}/tcp" "ufw 拒绝校验端点端口"
# 把 SSH 关在门外就等于把自己锁在门外，而这台机器可能在别的大洲
assert_contains "$UFW_PLAN" "ufw allow 22/tcp" "ufw 放行 SSH，别把自己锁在外面"

FW_PLAN="$(firewall_plan firewalld)"
assert_contains "$FW_PLAN" "firewall-cmd --permanent --add-service=http" "firewalld 放行 http"
assert_contains "$FW_PLAN" "firewall-cmd --permanent --add-service=https" "firewalld 放行 https"
assert_contains "$FW_PLAN" "firewall-cmd --reload" "firewalld 规则要 reload 才生效"
# firewalld 默认拒绝未放行端口，所以 Admin 端口不需要显式 deny——
# 但必须确认它没被别的服务顺带放行
assert_not_contains "$FW_PLAN" "--add-port=${CADDY_ADMIN_PORT}" "firewalld 不放行 Admin 端口"

NFT_PLAN="$(firewall_plan nftables)"
assert_contains "$NFT_PLAN" "tcp dport { 80, 443 } accept" "nftables 放行 80/443"
assert_contains "$NFT_PLAN" "tcp dport 22 accept" "nftables 放行 SSH"
assert_contains "$NFT_PLAN" "policy drop" "nftables 默认拒绝"
# 回环必须放行，否则 Agent 连不上本机 Caddy Admin，下发全部失败
assert_contains "$NFT_PLAN" 'iif "lo" accept' "nftables 放行回环"

assert_fails "不认识的防火墙后端被拒绝" firewall_plan iptables-legacy-something

# ── 幂等 ──
#
# 「重复执行不产生副作用」不能靠推理，得真跑。无脑重写会让每次执行都触发
# daemon-reload 与重启，而重启 Caddy 意味着断掉所有正在进行的连接——
# 「重跑一遍确认一下」不该有这种代价。
#
# 这里把单元目录指到临时目录，真写真比对。系统调用（systemctl / apt）验不了，
# 但「文件内容变没变」这个判据本身是可以验的，而它正是决定要不要重启的那个。

section "幂等"

TMPROOT="$(mktemp -d)"
trap 'rm -rf "$TMPROOT"' EXIT
SYSTEMD_DIR="$TMPROOT/systemd"

# 第一次：文件不存在 → 应报告「有变化」
if write_units node-hk-01 master.example.com:9000; then
  ok "首次写入报告有变化"
else
  fail "首次写入报告有变化" "返回了「无变化」"
fi
assert_succeeds "单元文件已生成" test -f "$SYSTEMD_DIR/edge-agent.service"
assert_succeeds "Caddy 覆盖已生成" test -f "$SYSTEMD_DIR/caddy.service.d/10-edge-hardening.conf"

# 第二次：内容相同 → 必须报告「无变化」，否则每次重跑都会重启 Caddy
if write_units node-hk-01 master.example.com:9000; then
  fail "重复执行报告无变化" "内容没变却报了「有变化」——每次重跑都会重启 Caddy，断掉所有连接"
else
  ok "重复执行报告无变化"
fi

# 参数变了 → 必须报告「有变化」，否则改了主控地址却不重启，配置与现实脱节
if write_units node-hk-01 other.example.com:9000; then
  ok "参数变化被识别为有变化"
else
  fail "参数变化被识别为有变化" "换了主控地址却报「无变化」"
fi

# set -e 之下重复执行不能把脚本打断——「无变化」是正常路径，不是错误
assert_succeeds "set -e 之下重复执行不中断" \
  bash -c "set -euo pipefail; export SYSTEMD_DIR='$SYSTEMD_DIR'; source '$HERE/edge-node.sh'; \
           write_units node-hk-01 other.example.com:9000 || true; echo done"

section "文件权限"

ENVP="$TMPROOT/agent.env"
EDGE_ENV_FILE="$ENVP"
write_if_changed "$ENVP" 0600 "$(agent_env_file tok-xyz)" >/dev/null || true
# 用 find -perm 而不是解析 ls：GNU 与 BSD 的 stat 参数不一样，而这个测试
# 在开发机（macOS）和 CI（Linux）上都要跑
assert_succeeds "凭据文件权限 0600（同机其他用户读不到）" \
  sh -c "find '$ENVP' -perm 600 | grep -q ."

printf '\n通过 %d，失败 %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
