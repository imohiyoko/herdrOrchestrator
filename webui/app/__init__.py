"""herdr Web UI の中身。入口は webui/server.py。

config → sanitize → herdr / data / jobs → views → handler の順に依存する(逆向きの import はしない)。
tasks.db は読むだけで、変更は tb を通す(actions.py)。直接書き込むのはログイン情報(run/)だけ(スケジュールの上書き設定は tb job で DB に書く。残った scheduler/settings/ のファイルは消す)。
"""
