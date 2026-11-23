#!/usr/bin/env python3
"""内容侧体例体检：每课的五个必备小节是否齐全。

    ## 判据        读完能自测的题，带折叠参考答案
    ## 配套实验    指向 labs/ 或写明「本课无实验」及理由
    ## 小结        三到五行，能当复查清单
    ## 思考        三问，开放式的
    ## 判据 里的答案必须是 ::: details 折叠（读者先自己想）

缺一节不会让构建失败，只会让读者少一道检验，或者提前看到答案。
这是 v1.4 立的体例，56 课逐课过了一遍。

用法：

    python3 scripts/check-lesson-shape.py
    python3 scripts/check-lesson-shape.py --root site/docs
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

LESSON_FILE_RE = re.compile(r"^\d{2}-[a-z0-9][a-z0-9-]*\.md$")
REQUIRED = ("## 判据", "## 配套实验", "## 小结", "## 思考")


def iter_lessons(root: Path):
    for path in sorted(root.rglob("*.md")):
        if LESSON_FILE_RE.match(path.name):
            yield path


def section(text: str, heading: str) -> str | None:
    """取出某个二级小节的正文，到下一个 ## 为止。"""
    i = text.find(heading + "\n")
    if i < 0:
        return None
    j = text.find("\n## ", i + len(heading))
    return text[i + len(heading) :] if j < 0 else text[i + len(heading) : j]


def check(root: Path) -> list[str]:
    problems: list[str] = []
    for path in iter_lessons(root):
        text = path.read_text(encoding="utf-8")
        rel = path.relative_to(root)
        missing = [h[3:] for h in REQUIRED if h + "\n" not in text]
        if missing:
            problems.append(f"{rel} → 缺小节：{'、'.join(missing)}")
            continue
        body = section(text, "## 判据") or ""
        if "::: details" not in body:
            problems.append(f"{rel} → 判据节缺 `::: details` 折叠的参考（开放式题也要给方向性提示）")
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
        print("发现体例问题：", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        return 1

    n = len(list(iter_lessons(args.root)))
    print(f"体例体检通过（{n} 课，五节齐全且答案已折叠）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
