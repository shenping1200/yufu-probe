#!/usr/bin/env bash
# 跨平台编译脚本：生成服务端 + 各平台 agent
set -e
cd "$(dirname "$0")"
mkdir -p dist

echo "==> build server"
env CGO_ENABLED=0 go build -o dist/probe-server ./server

echo "==> build agents"
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  if [ "$os" = "windows" ]; then ext=".exe"; fi
  echo "    agent $target"
  env CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o "dist/yufu-agent-$os-$arch$ext" ./agent
done

echo "==> done -> dist/"
ls -lh dist
