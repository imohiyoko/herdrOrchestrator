package tbcli

import (
	"regexp"
	"strconv"
)

// 承認待ちの agent の操作(L-<n>)の ID の確かめ。承認と却下は API(agent.approve / agent.reject。internal/app/agent/approve.go)。

var reLid = regexp.MustCompile(`^L-[0-9]+$`)

func validLid(s string) (int64, error) {
	if !reLid.MatchString(s) {
		return 0, usage("操作 ID は L-<数字>: %s", s)
	}
	n, _ := strconv.ParseInt(s[2:], 10, 64)
	return n, nil
}
