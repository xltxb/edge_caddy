#!/usr/bin/env bash
# 打后端的包：主控 + Agent，交叉编译到 Linux。
#
# **两个架构都出**（amd64 / arm64）：边缘节点是买来的 VPS，
# 架构不由我们决定，而问一次的成本高于多编一次。
#
# CGO_ENABLED=0 —— 依赖全是纯 Go，关掉之后二进制静态链接，
# **不挑目标机器的 glibc 版本**。一台 Debian 11 上编出来的动态二进制
# 放到 Alpine 上会直接跑不起来，而那个错误信息（"not found"）
# 看起来像文件不存在，不像 ABI 不兼容。
#
# 版本号用 -ldflags 注入：它会显示在控制台的节点列表上，
# 人靠它判断「我推上去的那一版到底上没上」。
set -euo pipefail

cd "$(dirname "$0")/.."
out="dist"
# **「脏」只看后端自己那几个目录。**
#
# `git describe --dirty` 看的是整个工作树，而这个仓库里住着两个 agent：
# 前端在 web/ 下有未提交的改动时，后端的包就会被标成 -dirty ——
# 而后端二进制里根本没有 web/ 的任何东西。
#
# 一个说「这个构建含未提交改动」而实际不含的版本戳，比没有版本戳更坏：
# 它会让人去找一个不存在的差异。**版本戳的作用域必须和产物的作用域一致。**
mine="cmd internal proto go.mod go.sum scripts deploy"
dirty=""
# shellcheck disable=SC2086
if ! git diff --quiet -- $mine 2>/dev/null || \
   ! git diff --cached --quiet -- $mine 2>/dev/null; then
  dirty="-dirty"
fi
version="${1:-$(git describe --tags --always 2>/dev/null || echo dev)${dirty}}"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
stamp="${version} (${commit})"

# **只清自己的产物，不要 rm -rf 整个 dist。**
#
# 前端的包也放在这里（同一个仓库，两个 agent）。`rm -rf dist` 会把
# `edge-console-*.tar.gz` 一起删掉——而那**不会报错**：下一个人打开 dist
# 只看到后端的包，会以为前端还没打，或者以为自己记错了。
mkdir -p "$out"
rm -rf "$out"/linux-* "$out"/edge-controller-*.tar.gz "$out"/SHA256SUMS

echo "版本：${stamp}"
echo

for arch in amd64 arm64; do
  d="$out/linux-$arch"
  mkdir -p "$d"

  # 二进制的名字**必须**是 edge-agent：控制台发给人的安装命令里写着
  # `--agent-bin ./edge-agent`，而那条命令与这个包是同一件事的两半
  # （internal/api/installcmd_test.go 盯着它们对得上）。
  for target in master:master agent:edge-agent; do
    src="cmd/${target%%:*}"
    bin="${target##*:}"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w -X 'main.Version=${stamp}'" \
      -o "$d/$bin" "./$src"
  done

  # 节点要的那两样一起放进去：脚本和二进制都是相对路径，
  # **谁也不负责把它们送上去**（deploy/README.md 开头那一段）。
  cp deploy/edge-node.sh "$d/"
  cp deploy/README.md "$d/部署说明.md"

  # **属主归零，不是只归模式。**
  #
  # 归档里每个条目都带着打包机器的 uid/gid。以 root 用 GNU tar 解包时，
  # 它会把**属主一起恢复**——于是一台 macOS 的 UID 501 / 组 staff
  # 会被印到目标机器上，而那台机器上根本没有 501 这个用户。
  #
  # 灰度上真实发生过（是前端那个包，形状完全一样）：`/opt` 变成
  # `drwx------ 501 staff`，**整台机器上任何非 root 用户都进不了 /opt**。
  #
  # 我第一次看自己的包时只看了顶层是不是 `./`，看到是 `linux-amd64/`
  # 就收手了——**没看属主那一列**。同形状的另一个不会自己浮出来，
  # 哪怕别人刚把机制原原本本讲给你听。
  #
  # 归零是「做不到那件事」，解包后 chown 是「记得做」。
  # 而 --uname/--gname 也要给：GNU tar 优先按**名字**解析，
  # 只归零数字 uid 的话，目标机器上若恰好有个叫 abiu 的用户就又落到他头上。
  ( cd "$out" && tar czf "edge-controller-${version}-linux-${arch}.tar.gz" \
      --uid 0 --gid 0 --uname root --gname root \
      "linux-$arch" )
  rm -rf "$d"
done

# **校验不过就不写校验和。** 一个没有校验和的包，是在说「别发它」。
#
# 前端 agent 的说法值得照抄：把「这次记得检查」换成「不检查就没有可发的东西」。
echo "检查包对目标机器有没有意见："
if ! python3 scripts/packcheck.py "$out"/edge-controller-*.tar.gz; then
  echo
  echo "包有问题，没有写 SHA256SUMS —— 别发它。" >&2
  exit 1
fi
echo

# 校验和也只算自己的：前端的包有它自己的 SHA256SUMS.web。
( cd "$out" && shasum -a 256 ./edge-controller-*.tar.gz > SHA256SUMS )

echo "产物："
ls -lh "$out"/*.tar.gz | awk '{printf "  %-52s %s\n", $9, $5}'
echo
echo "校验和：$out/SHA256SUMS"
echo
echo "主控只需要包里的 master；边缘节点需要 edge-agent + edge-node.sh。"
