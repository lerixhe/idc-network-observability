#!/usr/bin/env bash
# 容器创建后的预热：把每个 lab 的依赖先拉好，第一次 make run 不用等。
set -euo pipefail

cd "$(dirname "$0")/.."

for dir in labs/*/; do
	[ -f "${dir}go.mod" ] || continue
	echo "==> $(basename "$dir")"
	( cd "$dir" && go mod download && go vet ./... )
done

echo "环境就绪。先跑：cd labs/00-setup && make run"
