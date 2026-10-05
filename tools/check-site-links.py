#!/usr/bin/env python3
"""Check generated documentation links and redirects without fetching external sites."""

import os
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit

import yaml


SITE_PREFIX = "/coral/"


class Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.urls = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag in {"a", "link"} and attrs.get("href"):
            self.urls.append(attrs["href"])
        if tag in {"img", "script"} and attrs.get("src"):
            self.urls.append(attrs["src"])
        if tag == "meta" and attrs.get("http-equiv", "").lower() == "refresh":
            content = attrs.get("content", "")
            if "url=" in content.lower():
                self.urls.append(content.split("=", 1)[1].strip(" '\""))


def target_for(site, page, url):
    parsed = urlsplit(url)
    if parsed.netloc and (parsed.scheme != "https" or parsed.netloc != "cdknorow.github.io"):
        return None
    if parsed.scheme and parsed.scheme != "https":
        return None
    if not parsed.path:
        return None
    path = unquote(parsed.path)
    if path.startswith(SITE_PREFIX):
        target = site / path[len(SITE_PREFIX) :]
    elif path.startswith("/"):
        raise ValueError("site-root URL lacks /coral/ prefix: " + url)
    else:
        target = page.parent / path
    target = Path(os.path.realpath(target))
    if site != target and site not in target.parents:
        raise ValueError("URL escapes generated site: " + url)
    if target.is_dir() or path.endswith("/"):
        target /= "index.html"
    return target


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: check-site-links.py GENERATED_SITE_DIR")
    site = Path(sys.argv[1]).resolve()
    config = yaml.safe_load(Path("mkdocs.yml").read_text())
    redirects = next(plugin["redirects"]["redirect_maps"] for plugin in config["plugins"] if isinstance(plugin, dict) and "redirects" in plugin)
    problems = []
    pages = list(site.rglob("*.html"))
    for page in pages:
        html = page.read_text(encoding="utf-8")
        links = Links()
        links.feed(html)
        for url in links.urls:
            try:
                target = target_for(site, page, url)
            except ValueError as exc:
                problems.append(f"{page.relative_to(site)}: {exc}")
                continue
            if target is not None and not target.is_file():
                problems.append(f"{page.relative_to(site)}: {url} -> missing {target.relative_to(site)}")
    for old, new in redirects.items():
        old_page = site / old.removesuffix(".md") / "index.html"
        new_page = site / new.removesuffix(".md") / "index.html"
        if not old_page.is_file() or not new_page.is_file():
            problems.append(f"redirect missing: {old} -> {new}")
    if problems:
        print("\n".join(problems), file=sys.stderr)
        raise SystemExit(1)
    print(f"Validated {len(pages)} HTML pages and {len(redirects)} legacy redirects")


if __name__ == "__main__":
    main()
