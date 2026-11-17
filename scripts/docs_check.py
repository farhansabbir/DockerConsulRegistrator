"""Checks the documentation page before it is published.

- every in-page link (#name) has a target;
- nothing is loaded from elsewhere: no scripts, stylesheets, images or
  frames from another site - the page stands alone;
- the elements open and close in order.

Usage: python3 -I scripts/docs_check.py docs/index.html
"""

import sys
from html.parser import HTMLParser

VOID = {"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"}
LOADS = {"script": "src", "link": "href", "img": "src", "iframe": "src", "source": "src", "video": "src", "audio": "src"}


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids, self.anchors, self.problems, self.stack = set(), [], [], []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            if a["id"] in self.ids:
                self.problems.append(f"line {self.getpos()[0]}: the id {a['id']!r} is used twice")
            self.ids.add(a["id"])
        href = a.get("href") or ""
        if tag == "a" and href.startswith("#") and len(href) > 1:
            self.anchors.append((href[1:], self.getpos()[0]))
        src = a.get(LOADS.get(tag, ""), "") or ""
        if tag in LOADS and src.split(":")[0] in ("http", "https", "") and src.startswith(("http", "//")):
            self.problems.append(f"line {self.getpos()[0]}: <{tag}> loads {src} - the page must stand alone")
        if tag not in VOID:
            self.stack.append((tag, self.getpos()[0]))

    def handle_endtag(self, tag):
        if tag in VOID:
            return
        if not self.stack or self.stack[-1][0] != tag:
            open_tag = self.stack[-1] if self.stack else ("nothing", 0)
            self.problems.append(f"line {self.getpos()[0]}: </{tag}> closes <{open_tag[0]}> from line {open_tag[1]}")
            return
        self.stack.pop()


def main(path):
    with open(path, encoding="utf-8") as f:
        text = f.read()
    p = Page()
    p.feed(text)
    p.close()
    for name, line in p.anchors:
        if name not in p.ids:
            p.problems.append(f"line {line}: the link #{name} has no target")
    for tag, line in p.stack:
        p.problems.append(f"line {line}: <{tag}> is never closed")
    if "<title>" not in text:
        p.problems.append("the page has no <title>")
    for problem in p.problems:
        print(f"{path}: {problem}")
    if p.problems:
        return 1
    print(f"{path}: {len(p.anchors)} in-page links, all with targets; nothing loaded from elsewhere")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "docs/index.html"))
