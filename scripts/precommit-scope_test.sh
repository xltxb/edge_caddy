#!/usr/bin/env bash
# precommit-scope.sh 的测试。**在临时仓库里跑，不碰真仓库的索引。**
#
# 四条里最要紧的是第三条：**只有它证明钩子真的在跑**。
# 没有它的话，前两条「允许通过」会在钩子根本没装上时同样是绿的 ——
# 一个「不该触发」的期望，在装置没跑时是白送的。
set -u

here="$(cd "$(dirname "$0")" && pwd)"
hook="$here/precommit-scope.sh"
tmp="$(mktemp -d)"
trap "rm -rf '$tmp'" EXIT

cd "$tmp" || exit 1
git init -q .
git config user.email t@t
git config user.name t
mkdir -p web/src internal/api .git/hooks
cp "$hook" ./scope.sh && chmod +x ./scope.sh
printf '#!/usr/bin/env bash\nexec "%s/scope.sh"\n' "$tmp" > .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit

pass=0; fail=0
check() {
  local name="$1" want="$2" rc got
  git commit -qm x >/dev/null 2>&1; rc=$?
  got=ok; [ $rc -ne 0 ] && got=reject
  if [ "$got" = "$want" ]; then
    echo "  ✓ $name → $got"; pass=$((pass+1))
  else
    echo "  ✗ $name → 期望 $want，实际 $got"; fail=$((fail+1))
  fi
  git reset -q
}

echo b > web/src/seed.ts && git add -A && git commit -qm seed --no-verify

echo "  只碰自己那一侧"
echo a > internal/api/a.go && git add internal/api/a.go && check "纯后端" ok
echo b2 > web/src/b.ts && git add web/src/b.ts && check "纯前端" ok

echo "  跨接缝（git add -A 的症状）"
echo c > internal/api/c.go && echo d > web/src/d.ts && git add -A && check "跨接缝" reject

echo "  逃生口"
git add -A
if git commit -qm x --no-verify >/dev/null 2>&1; then
  echo "  ✓ --no-verify 仍可放行"; pass=$((pass+1))
else
  echo "  ✗ --no-verify 被挡住了 —— 一道关不掉的门迟早会被整个卸掉"; fail=$((fail+1))
fi

echo
if [ $fail -eq 0 ]; then
  echo "  ✓ 通过 $pass 条（含一条证明钩子真的在跑）"
else
  echo "  ✗ 通过 $pass 条，失败 $fail 条"
fi
[ $fail -eq 0 ]
