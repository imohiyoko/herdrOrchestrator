"""server.py の起動の選び方(既定は Go 版、古い・無い herdrctl なら Python 版、WEBUI_IMPL で固定)。"""
import atexit
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
import server  # noqa: E402

fails = []
tmp = tempfile.mkdtemp(prefix="webui-choose-")
atexit.register(shutil.rmtree, tmp, True)


def fake(name, help_text):
    p = os.path.join(tmp, name)
    with open(p, "w") as f:
        f.write(f"#!/bin/sh\necho '{help_text}'\n")
    os.chmod(p, 0o755)
    return p


new, old = fake("new", "  webui serve  Web UI"), fake("old", "  version  版")


def run(env):
    for k in ("WEBUI_IMPL", "WEBUI_HERDRCTL", "TB_HERDRCTL"):
        os.environ.pop(k, None)
    os.environ.update(env)
    os.environ["PATH"] = tmp + "/nowhere"
    got = []
    server.exec_go = lambda hc: got.append(hc) or (_ for _ in ()).throw(SystemExit(0))
    try:
        server.choose()
    except SystemExit as ex:
        return got[0] if got else f"exit:{ex.code}"
    return "python"


for name, env, want in [
    ("既定: Web UI を持つ herdrctl なら Go 版", {"WEBUI_HERDRCTL": new}, new),
    ("既定: TB_HERDRCTL でも", {"TB_HERDRCTL": new}, new),
    ("既定: 古い herdrctl なら Python 版", {"WEBUI_HERDRCTL": old}, "python"),
    ("既定: herdrctl が無ければ Python 版", {}, "python"),
    ("WEBUI_IMPL=python は herdrctl があっても Python 版", {"WEBUI_IMPL": "python", "WEBUI_HERDRCTL": new}, "python"),
    ("WEBUI_IMPL=go は確かめずに Go 版", {"WEBUI_IMPL": "go", "WEBUI_HERDRCTL": old}, old),
]:
    got = run(env)
    ok = got == want
    print(("ok   " if ok else "FAIL ") + name + ("" if ok else f"  [{got}]"))
    if not ok:
        fails.append(name)
got = run({"WEBUI_IMPL": "go"})
ok = isinstance(got, str) and got.startswith("exit:herdr-webui: WEBUI_IMPL=go だが herdrctl が見つかりません")
print(("ok   " if ok else "FAIL ") + "WEBUI_IMPL=go で herdrctl が無ければ止まる" + ("" if ok else f"  [{got}]"))
if not ok:
    fails.append("go-missing")
sys.exit(1 if fails else 0)
