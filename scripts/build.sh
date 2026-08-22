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
version="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
stamp="${version} (${commit})"

rm -rf "$out" && mkdir -p "$out"

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

  ( cd "$out" && tar czf "edge-controller-${version}-linux-${arch}.tar.gz" "linux-$arch" )
  rm -rf "$d"
done

( cd "$out" && shasum -a 256 ./*.tar.gz > SHA256SUMS )

echo "产物："
ls -lh "$out"/*.tar.gz | awk '{printf "  %-52s %s\n", $9, $5}'
echo
echo "校验和：$out/SHA256SUMS"
echo
echo "主控只需要包里的 master；边缘节点需要 edge-agent + edge-node.sh。"
