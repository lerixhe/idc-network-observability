#!/usr/bin/env python3
"""内容侧链接体检：扫 site/docs 下的 Markdown，检查站内相对链接指向的文件是否存在。

只查「文件在不在」这一层，不查锚点——锚点要靠构建产物再扫一遍，
阶段一到再补。用法：

    python3 scripts/check-links.py
    python3 scripts/check-links.py --root site/docs
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

# 行内链接与图片：[文字](目标) / ![alt](目标)
LINK_RE = re.compile(r"!?\[[^\]]*\]\(([^)\s]+)\)")


def iter_markdown(root: Path):
    yield from sorted(root.rglob("*.md"))


def resolve(src: Path, target: str, root: Path) -> list[Path] | None:
    """把链接解析成候选磁盘路径；外链、锚点、mailto 返回 None（不检查）。"""
    if target.startswith(("http://", "https://", "mailto:", "#")):
        return None
    path_part = target.split("#", 1)[0]
    if not path_part:
        return None

    if path_part.startswith("/"):
        # 站内绝对路径以 docs 为根
        base = root / path_part.lstrip("/")
    else:
        base = (src.parent / path_part).resolve()

    # VitePress 的 cleanUrls：/xx 可能对应 xx.md 或 xx/index.md
    if base.suffix == "":
        return [base, base.with_suffix(".md"), base / "index.md"]
    return [base]


def check(root: Path) -> list[str]:
    problems: list[str] = []
    for md in iter_markdown(root):
        text = md.read_text(encoding="utf-8")
        for target in LINK_RE.findall(text):
            cands = resolve(md, target, root)
            if cands is None:
                continue
            if any(p.exists() for p in cands):
                continue
            problems.append(f"{md.relative_to(root)} → {target}")
    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default="site/docs", type=Path)
    args = ap.parse_args()

    if not args.root.is_dir():
        print(f"目录不存在: {args.root}", file=sys.stderr)
        return 2

    problems = check(args.root)
    if problems:
        print("发现死链：", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        return 1

    print(f"链接体检通过（{len(list(iter_markdown(args.root)))} 篇）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
