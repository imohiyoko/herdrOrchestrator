// Package capv は「上限」の値の共通の読み方: 数のほかに unlimited(上限なし)を受け付ける。
// 上限のある設定(limits・spawn-policy・gate・notify・pr-queue・TB_*_MAX など)は、どれもこれで読み、
// 人が unlimited と書けば上限を外せるようにする。既定値は各設定が決める(ここでは変えない)。
package capv

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// Unlimited は「上限なし」の書き方。大文字小文字は問わない。
const Unlimited = "unlimited"

// NoLimit は上限なしを数で扱う所(残りの枠・切り詰めの長さなど)に渡す十分に大きい数。
const NoLimit = math.MaxInt32

// IsUnlimited は v が「上限なし」の書き方か。
func IsUnlimited(v string) bool { return strings.EqualFold(strings.TrimSpace(v), Unlimited) }

// Parse は上限の値を読む。limited=false なら上限なし。数でも unlimited でもなければ ok=false。
func Parse(v string) (n int, limited, ok bool) {
	if IsUnlimited(v) {
		return 0, false, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, true, false
	}
	return n, true, true
}

// Int は上限の値を数にする。上限なしは NoLimit、読めなければ def。
func Int(v string, def int) int {
	n, lim, ok := Parse(v)
	switch {
	case !ok:
		return def
	case !lim:
		return NoLimit
	}
	return n
}

// Env は環境変数の上限(TB_*_MAX など)を読む。空・読めない・負なら def、unlimited なら NoLimit。
func Env(k string, def int) int {
	n := Int(os.Getenv(k), def)
	if n < 0 {
		return def
	}
	return n
}

// Exceeds は n が上限 max(limited=false なら上限なし)を超えるか。
func Exceeds(n, max int, limited bool) bool { return limited && n > max }

// Show は上限を表示用の文字列にする(上限なしは「無制限」)。
func Show(max int, limited bool) string {
	if !limited {
		return "無制限"
	}
	return strconv.Itoa(max)
}
