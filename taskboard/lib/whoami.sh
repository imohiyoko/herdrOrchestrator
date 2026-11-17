# shellcheck shell=bash
# tb whoami: tb が今どこを向いているか(checkout・DB・herdr セッション・launchd ラベル・agent のペインか)と、それぞれの決まり方を出す。
# 読み取りだけ。DB を開かず(init_db の前に bin/tb が呼ぶ)、sqlite3・jq・herdr を使わず、ファイルを書かない。
# bin/tb から source される。単体では実行しない。

# 存在しなくてもよい path の実体(symlink を辿る)。macOS の readlink に -f が無いので手で辿る
_whoami_real() {
  local p=$1 d n=0
  while [[ -L $p && $n -lt 20 ]]; do d=$(cd "$(dirname "$p")" 2>/dev/null && pwd -P) || break; p=$(readlink "$p"); [[ $p == /* ]] || p=$d/$p; n=$((n+1)); done
  if d=$(cd "$(dirname "$p")" 2>/dev/null && pwd -P); then printf '%s/%s\n' "${d%/}" "$(basename "$p")"; else printf '%s\n' "$p"; fi
}

cmd_whoami() {
  local short=0
  case ${1:-} in
    "") ;;
    --short) short=1; shift;;
    *) usage "whoami [--short]";;
  esac
  [[ $# -eq 0 ]] || usage "whoami [--short]"

  local session ssrc sock=${HERDR_SOCKET_PATH:-}
  session=$(herdr_session_name); ssrc=$(herdr_session_source)
  if [[ $short -eq 1 ]]; then printf '%s\n' "$session"; return 0; fi

  local root db dbsrc dbreal inst isrc prefix perr=0 pane=${HERDR_PANE_ID:-} paneyn
  root=$(cd "$HERDR_ROOT" && pwd -P)
  if [[ -n ${TB_DB:-} ]]; then db=$TB_DB; dbsrc="env TB_DB"; else db=$HERDR_DEFAULT_TB_DB; dbsrc="既定(全セッション共有。行は herdr_session で分ける)"; fi
  dbreal=$(_whoami_real "$db")
  case $ssrc in
    socket)  ssrc="env HERDR_SOCKET_PATH";;
    env)     ssrc="env HERDR_SESSION";;
    file)    ssrc="ファイル $(herdr_session_file)";;
    default) ssrc="既定";;
  esac
  inst=${TB_INSTANCE:-}
  if [[ -n $inst ]]; then isrc="env TB_INSTANCE"; else inst="(未設定)"; isrc="既定"; fi
  prefix=$(herdr_label_prefix 2>/dev/null) || { perr=1; prefix="(TB_INSTANCE が不正)"; }
  if [[ -n $pane ]]; then paneyn="あり: $pane (agent のペイン)"; else paneyn="なし (人のシェル)"; fi

  printf '%-13s %s   [checkout]\n' checkout "$root"
  printf '%-13s %s   [%s]\n' db "$db" "$dbsrc"
  [[ $dbreal == "$db" ]] || printf '%-13s %s\n' "" "実体: $dbreal"
  [[ -e $db ]] || printf '%-13s %s\n' "" "(まだ無い。tb の他のコマンドが初回に作る)"
  [[ -n ${TB_DB:-} || ! -f $HERDR_LEGACY_TB_DB ]] || printf '%-13s %s\n' "" "旧形式の DB が残っています: $HERDR_LEGACY_TB_DB(取り込むには tb db-import)"
  printf '%-13s %s   [%s]\n' session "$session" "$ssrc"
  printf '%-13s %s\n' socket "${sock:-(未設定)}"
  printf '%-13s %s   [%s]\n' instance "$inst" "$isrc"
  printf '%-13s %s   [%s]\n' label-prefix "$prefix" "$isrc"
  printf '%-13s %s   [env HERDR_PANE_ID]\n' pane "$paneyn"

  # 食い違いは黙らせず notes に出す(見るだけ。何も変えない)
  local expect
  [[ $session == default ]] && expect="" || expect=$session
  if [[ $ssrc == "env HERDR_SOCKET_PATH" && -n ${HERDR_SESSION:-} && ${HERDR_SESSION} != "$session" ]]; then
    printf 'note: HERDR_SESSION=%s は HERDR_SOCKET_PATH(%s)に負けて効いていません\n' "$HERDR_SESSION" "$session"
  fi
  if [[ -n $sock && $ssrc != "env HERDR_SOCKET_PATH" ]]; then
    printf 'note: HERDR_SOCKET_PATH(%s)の配置から名前を導けないため、セッション名は [%s] から決めています\n' "$sock" "$ssrc"
  fi
  [[ $perr -eq 1 ]] && printf 'note: TB_INSTANCE は英数字・. _ - の 64 字以内にしてください(今の値: %s)\n' "${TB_INSTANCE:-}"
  if [[ $perr -eq 0 && ${TB_INSTANCE:-} != "$expect" ]]; then
    if [[ -z ${TB_INSTANCE:-} ]]; then
      printf 'note: セッション %s ですが TB_INSTANCE が未設定のため、launchd ラベルは従来の %s のままです(同じ接頭辞を使う他の checkout と名前空間を共有します)\n' "$session" "$prefix"
    else
      printf 'note: TB_INSTANCE=%s がセッション %s と対応していません(default は TB_INSTANCE を渡さず、他は TB_INSTANCE=セッション名が設計の既定)\n' "$TB_INSTANCE" "$session"
    fi
  fi
  return 0
}
