#!/usr/bin/env python3

import sys

import markdown


def main() -> int:
    source = sys.stdin.read()
    html = markdown.markdown(
        source,
        extensions=[
            "fenced_code",
            "tables",
            "toc",
            "sane_lists",
        ],
        output_format="html5",
    )
    sys.stdout.write(html)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
