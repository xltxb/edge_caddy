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
exit 0
