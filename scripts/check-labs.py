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

# labs/00-setup、labs/15-tools-parse 这种两段式编号名；
# 习题目录也纳入，且与参考实现分属两个命名空间：labs/exercises/01-portstate
LAB_REF_RE = re.compile(r"labs/(exercises/)?(\d{2}-[a-z0-9][a-z0-9-]*)")


def iter_markdown(root: Path):
    yield from sorted(root.rglob("*.md"))


def check(root: Path, labs: Path) -> list[str]:
    problems: list[str] = []
    def known_dirs(base: Path) -> set[str]:
        return {p.name for p in base.iterdir() if p.is_dir()} if base.is_dir() else set()

    refs = known_dirs(labs)
    exes = known_dirs(labs / "exercises")
    for md in iter_markdown(root):
        text = md.read_text(encoding="utf-8")
        for line_no, line in enumerate(text.splitlines(), 1):
            for m in LAB_REF_RE.finditer(line):
                sub, name = m.group(1) or "", m.group(2)
                pool = exes if sub else refs
                if name not in pool:
                    where = f"labs/{sub}{name}"
                    problems.append(f"{md.relative_to(root)}:{line_no} → {where}（目录不存在）")
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

    refs, exes = set(), set()
    for md in iter_markdown(args.root):
        for m in LAB_REF_RE.finditer(md.read_text(encoding="utf-8")):
            (exes if m.group(1) else refs).add(m.group(2))
    print(f"lab 引用体检通过（参考实现 {len(refs)} 个、习题 {len(exes)} 个被引用）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
