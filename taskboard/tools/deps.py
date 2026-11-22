#!/usr/bin/env python3
"""taskboard/lib/*.sh のモジュール間の依存を数える(ADR 0004)。

使い方:
  python3 taskboard/tools/deps.py            # 依存の一覧と、層の向きに反する依存の数
  python3 taskboard/tools/deps.py --check    # 許容リスト(deps-allow.txt)に無い違反があれば終了コード 1

数え方: 各ファイルの先頭の列で定義された関数名(`name() {`)が、別のファイルの
コメント以外の場所に「単語として」出てきたら、そのファイルの組で 1 本と数える。
名前の一致で数えるので、同じ名前の変数などを拾うことがある(目安)。
"""
import collections, os, re, sys

HERE = os.path.dirname(os.path.abspath(__file__))
LIB = os.path.join(HERE, '..', 'lib')
ALLOW = os.path.join(HERE, 'deps-allow.txt')

# 層(ADR 0004)。下の層は上の層を呼ばない。機能どうしは互いを呼ばない(許容リストで管理)
KERNEL = {'db', 'core', 'config', 'transport', 'kinds', 'util', 'identity', 'perms'}
BUS = {'tasks', 'comments', 'notify', 'notifyrules'}

def layer(m):
    return 'kernel' if m in KERNEL else 'bus' if m in BUS else 'feature'

def strip_comments(text):
    out = []
    for line in text.splitlines():
        if line.lstrip().startswith('#'):
            continue
        out.append(re.sub(r'\s#\s.*$', '', line))
    return '\n'.join(out)

def load():
    src = {}
    for f in sorted(os.listdir(LIB)):
        if f.endswith('.sh'):
            with open(os.path.join(LIB, f), encoding='utf-8') as fh:
                src[f[:-3]] = fh.read()
    defs = {}
    for m, s in src.items():
        for fn in re.findall(r'^([A-Za-z_][\w-]*)\(\)\s*\{', s, re.M):
            defs.setdefault(fn, m)
    edges = collections.defaultdict(collections.Counter)
    for m, s in src.items():
        body = strip_comments(s)
        for tok in re.findall(r'(?<![\w@./$-])([A-Za-z_][\w-]*)(?![\w.-])', body):
            d = defs.get(tok)
            if d and d != m:
                edges[m][(d, tok)] += 1
    return src, edges

RANK = {'kernel': 0, 'bus': 1, 'feature': 2}

def violations(edges):
    v = set()
    for m, c in edges.items():
        for (d, fn) in c:
            lm, ld = layer(m), layer(d)
            if RANK[ld] > RANK[lm] or (lm == ld == 'feature'):
                v.add(f'{m} -> {d}')
    return v

def main():
    src, edges = load()
    mods = collections.defaultdict(set)
    for m, c in edges.items():
        for (d, _) in c:
            mods[m].add(d)
    total = sum(len(x) for x in mods.values())
    v = violations(edges)
    if '--check' in sys.argv:
        allow = set()
        if os.path.exists(ALLOW):
            with open(ALLOW, encoding='utf-8') as fh:
                allow = {l.strip() for l in fh if l.strip() and not l.startswith('#')}
        new = sorted(v - allow)
        gone = sorted(allow - v)
        for e in new:
            print(f'deps: 層の向きに反する新しい依存: {e}(ADR 0004。土台に移すか、チケットのイベントでつなぐ)', file=sys.stderr)
        for e in gone:
            print(f'deps: 許容リストの {e} はもう無いので taskboard/tools/deps-allow.txt から消してください', file=sys.stderr)
        sys.exit(1 if new or gone else 0)
    for m in sorted(src):
        ds = sorted(mods[m])
        print(f'{m:12} [{layer(m):7}] {len(ds):2} -> {" ".join(ds)}')
    print(f'ファイルの組の依存: {total} 本 / 層の向きに反する依存: {len(v)} 本')
    if '--list' in sys.argv:
        print('\n'.join(sorted(v)))

if __name__ == '__main__':
    main()
