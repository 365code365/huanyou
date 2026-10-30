#!/usr/bin/env python3
"""Collect every translatable Chinese string into server/internal/i18n/base.json.

Sources:
  app/lib/**/*.dart              string literals containing Chinese (tr() keys, enum labels)
  server/internal/api/*.go       user-facing error messages (writeErr(..., "中文")), admin files / handlers excluded

The server embeds the result: the admin panel shows it as the translation
template and the AI translator works from it. Re-run after changing app text:

  python3 scripts/i18n-extract.py
"""
import glob
import json
import os
import re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CJK = re.compile(r'[\u4e00-\u9fff]')
OUT = os.path.join(ROOT, 'server/internal/i18n/base.json')
SKIP_DART = {'lib/core/config.dart'}  # brand name stays as is


def dart_unescape(s):
    return re.sub(r"\\(.)", lambda m: {'n': '\n', 't': '\t'}.get(m.group(1), m.group(1)), s)


def dart_literals(src):
    """Single-quoted, non-raw literals outside comments, without interpolation."""
    i, n = 0, len(src)
    while i < n:
        if src.startswith('//', i):
            j = src.find('\n', i)
            i = n if j < 0 else j
            continue
        if src.startswith('/*', i):
            i = src.find('*/', i) + 2
            continue
        c = src[i]
        if c in '"\'':
            raw = i > 0 and src[i - 1] == 'r'
            j = i + 1
            while src[j] != c and src[j] != '\n':
                j += 2 if src[j] == '\\' else 1
            body = src[i + 1:j]
            if not raw and '$' not in body.replace('\\$', ''):
                yield dart_unescape(body)
            i = j + 1
            continue
        i += 1


def main():
    keys = set()
    for f in glob.glob(os.path.join(ROOT, 'app/lib/**/*.dart'), recursive=True):
        rel = os.path.relpath(f, os.path.join(ROOT, 'app'))
        if rel in SKIP_DART:
            continue
        for s in dart_literals(open(f, encoding='utf-8').read()):
            if CJK.search(s):
                keys.add(s)
    for f in glob.glob(os.path.join(ROOT, 'server/internal/api/*.go')):
        name = os.path.basename(f)
        if name.startswith('admin') or name.endswith('_test.go'):
            continue
        src = open(f, encoding='utf-8').read()
        # Drop admin handlers (func (s *Server) adminXxx): the panel is Chinese-only.
        chunks = re.split(r'\n(?=func )', src)
        src = '\n'.join(c for c in chunks if not re.match(r'func \(\w+ \*?\w+\) admin', c))
        for m in re.finditer(r'writeErr\(w, [^,]+, "[^"]*", "((?:[^"\\]|\\.)*)"\)', src):
            s = json.loads('"%s"' % m.group(1))
            if CJK.search(s):
                keys.add(s)
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, 'w', encoding='utf-8') as fh:
        json.dump(sorted(keys), fh, ensure_ascii=False, indent=1)
        fh.write('\n')
    print('%d strings -> %s' % (len(keys), os.path.relpath(OUT, ROOT)))


if __name__ == '__main__':
    main()
