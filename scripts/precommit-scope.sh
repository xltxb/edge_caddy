#!/usr/bin/env bash
# 拒绝跨越前后端接缝的提交。
#
# 这个仓库是**同一个工作树、两个 agent**：后端管 cmd/ internal/ proto/
# migrations/ docs/ scripts/ deploy/，前端管 web/（见 CLAUDE.md）。
#
# 今天这条规矩被破了两次，方向相反：
#
#   早上  git checkout web/src/model.ts  → 对方未提交的改动被冲掉，丢了
#   晚上  git add -A                     → 对方未提交的改动被收进我的提交
#
# 第二次没丢数据，代价是**那批改动的理由没进版本库**：提交信息说的是
# 脚本输出，底下躺着证书页重构。下一个读它的人会被这条信息送错方向。
#
# **同一个根：git 操作的作用域覆盖了别人的目录。**
# 「提交前看一眼 git status」是一句承诺，而今天整天在讲承诺不算处置 ——
# 一个检查的有效性取决于它挂在哪条路径上，而提交就是改动离开这里的那一步。
#
# 要真的跨接缝提交（几乎不该有）：git commit --no-verify。
set -u

staged="$(git diff --cached --name-only)"
[ -z "$staged" ] && exit 0

web="$(printf '%s\n' "$staged" | grep '^web/' || true)"
other="$(printf '%s\n' "$staged" | grep -v '^web/' || true)"

# ---- 一、跨接缝的提交 --------------------------------------------------
if [ -n "$web" ] && [ -n "$other" ]; then
  echo "✗ 这次提交同时动了 web/ 和它外面的东西 —— 两个 agent 共用一个工作树，"
  echo "  这多半是 'git add -A' 把对方未提交的改动一起收走了。"
  echo
  echo "  web/ 里的（$(printf '%s\n' "$web" | wc -l | tr -d ' ') 个）："
  printf '%s\n' "$web" | sed 's/^/    /' | head -8
  [ "$(printf '%s\n' "$web" | wc -l)" -gt 8 ] && echo "    …"
  echo
  echo "  外面的（$(printf '%s\n' "$other" | wc -l | tr -d ' ') 个）："
  printf '%s\n' "$other" | sed 's/^/    /' | head -8
  [ "$(printf '%s\n' "$other" | wc -l)" -gt 8 ] && echo "    …"
  echo
  echo "  逐个 add 自己那一侧的文件，别用 -A 或 '.'。"
  echo "  真要跨接缝：git commit --no-verify"
  exit 1
fi

# ---- 二、有没有一份对得上当前代码的绿色测试凭据 -------------------------
#
# **我在红的状态下提交过三次。** 每次形状都一样：把 gotest.py 和 git commit
# 串在一条命令里，只读了 commit 的结果。判词挪到最后一行解决的是
# 「读到假的绿」，解决不了「压根没看」。
#
# 而把 70 秒的全量测试挂进这里，是我自己警告过的那种门：
# **昂贵到让人绕开，等于没有门，而且比没有门更糟。**
#
# 所以这道门**不重跑测试**，只查凭据（gotest.py 跑完写的，按 .go 内容哈希）。
# 成本是几十毫秒。跑完之后又改了代码，哈希就对不上 —— 那正是
# 「跑过了，但那不是这一版」这一档。
root="$(git rev-parse --show-toplevel)"

if printf '%s\n' "$staged" | grep -q '\.go$'; then
  if [ ! -f "$root/scripts/gotest.py" ]; then
    # **跳过要说出来。** 静默跳过的话，一个装错地方的钩子
    # 会看起来在守着，而它什么也没做 —— 这一整天数的就是这个形状。
    echo "  ⚠ 找不到 $root/scripts/gotest.py，跳过测试凭据检查" >&2
  elif ! python3 "$root/scripts/gotest.py" --check-stamp; then
    echo
    echo "  这次提交动了 Go 代码，而没有一份对得上它的绿色测试凭据。" >&2
    exit 1
  fi
fi

# ---- 三、几个便宜的检查（合计约 1.2 秒） -------------------------------
#
# 它们不需要数据库和 Caddy，跑得起来的地方就跑得动。
# gotest 那 70 秒不挂在这里，理由见上。
for chk in comments humantext unread; do
  if [ ! -f "$root/scripts/$chk.py" ]; then
    echo "  ⚠ 找不到 $root/scripts/$chk.py，跳过" >&2
    continue
  fi
  if ! python3 "$root/scripts/$chk.py" >/dev/null 2>&1; then
    echo "  ✗ $chk 没过 —— 跑一遍 python3 scripts/$chk.py 看它说什么" >&2
    exit 1
  fi
done
exit 0
