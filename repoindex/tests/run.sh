#!/bin/bash
# repoindex のテスト。DB は一時ディレクトリ。ネットワーク・モデルは使わない。
set -u
cd "$(dirname "${BASH_SOURCE[0]}")/.."
python3 -m py_compile bin/repoindex ri/*.py || exit 1
exec python3 -m unittest discover -s tests -p 'test_*.py' -v
