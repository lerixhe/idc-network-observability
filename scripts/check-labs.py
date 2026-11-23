#!/usr/bin/env python3
"""内容侧 lab 引用体检：扫 site/docs 下的 Markdown，检查正文引用的

    labs/01-portstate

这类路径在 `labs/` 下真实存在。只查「目录在不在」，不查 README 内容。

为什么单独一个脚本：`check-links.py` 只认 Markdown 链接，`labs/xx` 在正文里
是裸代码串（反引号内），不在它的射程里。课号重排过两次，这类引用最容易漂。

用法：

    python3 scripts/check-labs.py
    python3 scripts/check-labs.py --root site/docs --labs labs
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

# labs/00-setup、labs/15-tools-parse 这种两段式编号名
LAB_REF_RE = re.compile(r"labs/(\d{2}-[a-z0-9][a-z0-9-]*)")


def iter_markdown(root: Path):
    yield from sorted(root.rglob("*.md"))


def check(root: Path, labs: Path) -> list[str]:
    problems: list[str] = []
    known = {p.name for p in labs.iterdir() if p.is_dir()} if labs.is_dir() else set()
    for md in iter_markdown(root):
        text = md.read_text(encoding="utf-8")
        for line_no, line in enumerate(text.splitlines(), 1):
            for name in LAB_REF_RE.findall(line):
                if name not in known:
                    problems.append(
                        f"{md.relative_to(root)}:{line_no} → labs/{name}（目录不存在）"
                    )
    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default="site/docs", type=Path)
    ap.add_argument("--labs", default="labs", type=Path)
    args = ap.parse_args()

    if not args.root.is_dir():
        print(f"目录不存在: {args.root}", file=sys.stderr)
        return 2
    if not args.labs.is_dir():
        print(f"目录不存在: {args.labs}", file=sys.stderr)
        return 2

    problems = check(args.root, args.labs)
    if problems:
        print("发现指向不存在实验的引用：", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        return 1

    names = len({n for md in iter_markdown(args.root) for n in LAB_REF_RE.findall(md.read_text(encoding="utf-8"))})
    print(f"lab 引用体检通过（{names} 个不同的实验被引用）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
