#!/usr/bin/env python3
"""内容侧课号体检：三件事。

1. 「第 N 课」的 N 必须落在现有课号范围内——课号重排后最容易漏改的就是这种引用
2. 每篇课的 frontmatter title 必须以「NN · 」开头，且 NN 与文件名前缀一致
3. 每个模块目录都有 index.md，且 index.md 课程表里列出的课号与目录里的文件一致

第 3 条查的是「篇首导读漏了新加的课」这类漂移，它不会让构建失败，只会让读者少看见一课。

用法：

    python3 scripts/check-lessons.py
    python3 scripts/check-lessons.py --root site/docs
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

LESSON_REF_RE = re.compile(r"第\s*(\d{1,2})\s*课")
LESSON_FILE_RE = re.compile(r"^(\d{2})-[a-z0-9][a-z0-9-]*\.md$")
TITLE_RE = re.compile(r"^title:\s*(\d{2})\s*·", re.MULTILINE)
# 篇首导读里的课程表行：| 04 | [标题](./04-port-down-causes) |
TABLE_ROW_RE = re.compile(r"^\|\s*(\d{2})\s*\|\s*\[")


def iter_markdown(root: Path):
    yield from sorted(root.rglob("*.md"))


def lesson_numbers(root: Path) -> tuple[set[int], dict[str, list[int]]]:
    """返回全部课号，以及每个模块目录里的课号列表。"""
    all_numbers: set[int] = set()
    by_module: dict[str, list[int]] = {}
    for path in sorted(root.rglob("*.md")):
        m = LESSON_FILE_RE.match(path.name)
        if not m:
            continue
        n = int(m.group(1))
        all_numbers.add(n)
        by_module.setdefault(path.parent.name, []).append(n)
    return all_numbers, by_module


def check_refs(root: Path, numbers: set[int]) -> list[str]:
    problems: list[str] = []
    if not numbers:
        return ["没有找到任何课文件，检查 --root"]
    lo, hi = min(numbers), max(numbers)
    for md in iter_markdown(root):
        for line_no, line in enumerate(md.read_text(encoding="utf-8").splitlines(), 1):
            for raw in LESSON_REF_RE.findall(line):
                n = int(raw)
                if n < lo or n > hi:
                    problems.append(
                        f"{md.relative_to(root)}:{line_no} → 第 {n:02d} 课（课号范围 {lo:02d}–{hi:02d}）"
                    )
    return problems


def check_titles(root: Path) -> list[str]:
    problems: list[str] = []
    for path in sorted(root.rglob("*.md")):
        m = LESSON_FILE_RE.match(path.name)
        if not m:
            continue
        want = m.group(1)
        head = path.read_text(encoding="utf-8")[:2000]
        title = TITLE_RE.search(head)
        if not title:
            problems.append(f"{path.relative_to(root)} → frontmatter 缺「title: NN · …」")
            continue
        if title.group(1) != want:
            problems.append(
                f"{path.relative_to(root)} → title 写的是 {title.group(1)}，文件名是 {want}"
            )
    return problems


def check_module_index(root: Path, by_module: dict[str, list[int]]) -> list[str]:
    problems: list[str] = []
    for module, numbers in sorted(by_module.items()):
        index = root / module / "index.md"
        if not index.exists():
            problems.append(f"{module}/ → 缺 index.md")
            continue
        listed = {
            int(m.group(1))
            for line in index.read_text(encoding="utf-8").splitlines()
            if (m := TABLE_ROW_RE.match(line.strip()))
        }
        missing = sorted(set(numbers) - listed)
        if missing:
            problems.append(
                f"{module}/index.md → 课程表漏了 "
                + "、".join(f"{n:02d}" for n in missing)
            )
    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default="site/docs", type=Path)
    args = ap.parse_args()

    if not args.root.is_dir():
        print(f"目录不存在: {args.root}", file=sys.stderr)
        return 2

    numbers, by_module = lesson_numbers(args.root)
    problems = check_refs(args.root, numbers) + check_titles(args.root) + check_module_index(args.root, by_module)

    if problems:
        print("发现课号问题：", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        return 1

    print(f"课号体检通过（{len(numbers)} 课，{len(by_module)} 个模块）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
