#!/usr/bin/env python3
"""会話の学習(#97)で使う文字列の小道具。tb の lib/convlearn.sh / convstage.sh から呼ぶ。標準ライブラリだけ。

  convtext.py ratio <下書きのファイル> <最終文のファイル>
      「編集の割合 下書きの文字数 送った文の文字数」を出す。編集の割合 = 文字単位の編集距離 / 長い方の文字数(0.000〜1.000)。両方空なら 0。
  convtext.py sanitize [--keep-month] [--deny 語,語,...]   (標準入力 → 標準出力)
      URL・メール・@名前・チケットやレビューの番号・日付・数字・拒否語を置き換える。改行とタブは空白 1 つに。
      --keep-month なら日付は年月(YYYY-MM)まで残す(組の記録の状況メモ用。パターンには使わない)。
  convtext.py scan [--deny 語,語,...]   (標準入力)
      パターンに残してはいけないものを探す。見つかれば「種類<TAB>該当」を 1 行ずつ出して終了コード 1、無ければ 0。
      種類: url email mention ref number name deny quote

名前と組織名は一般には見分けられない。ここでは「英字の大文字で始まる語」を名前の疑いとして弾き(name)、
人が conv-learn の deny = に書いた語を拒否語として弾く(deny)。誤って弾いたときは人が言い換える(緩める方向に倒さない)。
"""
import re
import sys

URL = re.compile(r'(?:https?|ftp)://\S+|www\.\S+', re.I)
EMAIL = re.compile(r'[\w.+-]+@[\w-]+(?:\.[\w-]+)+')
MENTION = re.compile(r'(?<![\w@])@[\w.-]+')
# チケット(T-x-001 / ABC-123)、#123、PR 123 / review 45 のような番号
REF = re.compile(r'\b[A-Za-z][A-Za-z0-9_]*-(?:[A-Za-z0-9_]+-)?\d+\b|#\d+|\b(?:PR|MR|issue|review|ticket)\s*\d+', re.I)
DATE = re.compile(r'(\d{4})[-/年.](\d{1,2})(?:[-/月.](\d{1,2})日?)?')
NUMBER = re.compile(r'\d+(?:[.,]\d+)*')
NAME = re.compile(r'(?<![A-Za-z])[A-Z][a-z]+(?:[A-Z][a-z]+)*\b')
# 「」や "" の引用が長すぎるもの(言い回しの引用はパターンの説明に要る分だけ)
QUOTE = re.compile(r'「([^」]{31,})」|"([^"]{31,})"')


def deny_words(args):
    if '--deny' in args:
        i = args.index('--deny')
        if i + 1 < len(args):
            return [w.strip() for w in args[i + 1].split(',') if w.strip()]
    return []


def levenshtein(a, b):
    if len(a) < len(b):
        a, b = b, a
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


def ratio(df, ff):
    with open(df, encoding='utf-8') as f:
        d = f.read().strip()
    with open(ff, encoding='utf-8') as f:
        g = f.read().strip()
    n = max(len(d), len(g))
    if n == 0:
        return 0.0, 0, 0
    dl, gl = len(d), len(g)
    # 長すぎる本文は先頭だけで測る(計算量を抑える。会話の下書きはふつう数百字)
    d, g = d[:4000], g[:4000]
    n = max(len(d), len(g))
    return levenshtein(d, g) / n, dl, gl


def sanitize(text, keep_month=False, deny=()):
    t = re.sub(r'[\t\r\n]+', ' ', text).replace('|', '/')
    t = URL.sub('<url>', t)
    t = EMAIL.sub('<email>', t)
    t = MENTION.sub('<name>', t)
    t = REF.sub('<ref>', t)
    if keep_month:
        t = DATE.sub(lambda m: '%s-%02d' % (m.group(1), int(m.group(2))), t)
        # 年月だけは残し、ほかの数字は消す
        parts = re.split(r'(\d{4}-\d{2})', t)
        t = ''.join(p if re.fullmatch(r'\d{4}-\d{2}', p) else NUMBER.sub('<n>', p) for p in parts)
    else:
        t = DATE.sub('<date>', t)
        t = NUMBER.sub('<n>', t)
    for w in deny:
        t = re.sub(re.escape(w), '<name>', t, flags=re.I)
    t = NAME.sub('<name>', t)
    return re.sub(r' {2,}', ' ', t).strip()


def scan(text, deny=()):
    found = []
    for kind, rx in (('url', URL), ('email', EMAIL), ('mention', MENTION), ('ref', REF), ('number', NUMBER), ('name', NAME)):
        for m in rx.finditer(text):
            found.append((kind, m.group(0)))
    for w in deny:
        if re.search(re.escape(w), text, re.I):
            found.append(('deny', w))
    for m in QUOTE.finditer(text):
        found.append(('quote', (m.group(1) or m.group(2))[:20] + '…'))
    return found


def main(argv):
    if len(argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    cmd, args = argv[1], argv[2:]
    if cmd == 'ratio' and len(args) == 2:
        print('%.3f %d %d' % ratio(args[0], args[1]))
        return 0
    if cmd == 'sanitize':
        print(sanitize(sys.stdin.read(), '--keep-month' in args, deny_words(args)))
        return 0
    if cmd == 'scan':
        found = scan(sys.stdin.read(), deny_words(args))
        for k, v in found:
            print('%s\t%s' % (k, v.replace('\t', ' ').replace('\n', ' ')))
        return 1 if found else 0
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == '__main__':
    sys.exit(main(sys.argv))
