#!/usr/bin/env bash
#
# 生成 nginx 的 Cloudflare 可信来源表，让审计日志记下真实访问者的 IP。
#
# **在 CDN 后面时，$remote_addr 是 CDN 边缘节点的地址，不是访问者的。**
# 不处理的话审计里每一条都记着 CDN 的 IP —— 那比记 nginx 自己还糟一点：
# 它看起来像一个真实的公网地址，人不会怀疑它。
#
# 用法：
#   sudo ./cf-realip.sh              # 生成 + nginx -t + reload
#   sudo ./cf-realip.sh --dry-run    # 只打印，不写文件
#
# 建议挂个定时任务每月跑一次（Cloudflare 的地址段会变）：
#   sudo cp deploy/cf-realip.sh /usr/local/sbin/
#   printf '%s\n' '17 4 1 * * root /usr/local/sbin/cf-realip.sh >/dev/null' \
#     > /etc/cron.d/cf-realip
#
# **不要手抄一份 IP 段进 nginx 配置。** 它会过期，而过期的表现是
# 「某些访问者的 IP 又变回 CDN 的地址」—— 没有任何东西会报错。
set -euo pipefail

OUT=/etc/nginx/conf.d/cloudflare-realip.conf
DRY=no
[ "${1:-}" = "--dry-run" ] && DRY=yes

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m错误:\033[0m %s\n' "$*" >&2; exit 1; }

fetch() {
  curl -fsS --max-time 20 "$1" || die "取不到 $1 —— 没有改动任何东西"
}

log "从 Cloudflare 取地址段"
v4="$(fetch https://www.cloudflare.com/ips-v4)"
v6="$(fetch https://www.cloudflare.com/ips-v6)"

# **每一行都得像个 CIDR，否则一个字也不写。**
#
# 这一步是承重的：这个文件的内容会变成「谁的 CF-Connecting-IP 值得采信」。
# 取回来的东西要是一页 HTML 错误页，而我们照单写进去，
# nginx 要么起不来（好），要么把一段谁都能落进去的范围当成可信（很坏）。
#
# 校验不过就不写，跟 packcheck.py 那条一样：
# **一份没通过校验的产物，是在说「别用它」。**
tmp="$(mktemp)"
# 双引号：路径在**设置 trap 的这一刻**就展开进去。
# 这里 $tmp 是全局的，单引号其实也对——写成这样是为了让两个脚本
# 只有一种写法。edge-node.sh 里那个是 local 的，单引号会在退出时
# 报 unbound variable，而那句话出现在**所有检查都 ✓ 之后**，
# 还会把退出码变成非零。**同一种形式，就不需要每次去想作用域。**
trap "rm -f '$tmp'" EXIT
n=0
while IFS= read -r cidr; do
  [ -n "$cidr" ] || continue
  case "$cidr" in
    *[!0-9a-fA-F.:/]*) die "取回来的内容里有不像 CIDR 的行：$cidr" ;;
    */*) ;;
    *)   die "取回来的内容里有不带前缀长度的行：$cidr" ;;
  esac
  printf 'set_real_ip_from %s;\n' "$cidr" >> "$tmp"
  n=$((n+1))
done <<EOF
$v4
$v6
EOF

# **先证明自己不瞎。** Cloudflare 的列表历来有 20 条上下；
# 少于 10 条说明取回来的不是那张表，而一个只有两三条的可信表
# 会让大部分访问者的 IP 悄悄退回 CDN 地址 —— 不报错，只是不再准。
[ "$n" -ge 10 ] || die "只解析出 $n 条地址段，太少了，取回来的多半不是那张表"
log "解析出 $n 条地址段"

if [ "$DRY" = yes ]; then
  cat "$tmp"
  exit 0
fi

[ "$(id -u)" = 0 ] || die "需要 root（要写 $OUT 并 reload nginx）"

# 先落到临时位置、验过再换上去：nginx -t 不过时旧文件原封不动。
install -m 0644 "$tmp" "$OUT.new"
mv "$OUT.new" "$OUT"
if ! nginx -t; then
  rm -f "$OUT"
  die "nginx -t 没过，已经把 $OUT 删掉了。删掉而不是留着，是因为
留着一份 nginx 拒绝加载的配置，下一次有人 reload 时会被这份文件挡住，
而那时他改的是别的东西 —— 排查方向完全错了。"
fi
nginx -s reload
log "已写入 $OUT 并 reload"
log "验一眼：登录控制台，然后看审计页的来源 IP 是不是你自己的公网地址"
