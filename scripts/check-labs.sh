#!/usr/bin/env bash
# 每个 lab：go vet + go test，跑通即算通过。
# 新增 lab 只要在 labs/ 下建目录并带上 go.mod，脚本自动纳入。
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0
for dir in labs/*/; do
	[ -f "${dir}go.mod" ] || continue
	echo "==> ${dir}"
	( cd "$dir" && go vet ./... && go test ./... ) || fail=1
done

if [ "$fail" -ne 0 ]; then
	echo "有 lab 未通过" >&2
	exit 1
fi
echo "全部 lab 通过"
