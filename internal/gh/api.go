package gh

import (
	"net/url"
	"regexp"
	"strings"
)

// tb gh api <gh api の引数...>: 呼び方で操作を分ける(既定は perms/ops.tbl)。
//   gh:api-get     GET の REST(ファイルを送らないもの)            allow
//   gh:api-write   GET 以外の REST(POST / PATCH / PUT / DELETE)   ask
//   gh:api-graphql graphql(query と mutation を見分けない)        ask
//   gh:api-admin   秘密・鍵・webhook・権限・runner・協力者・リポジトリの削除、
//                  approve・マージの API(専用の gh:review-approve / gh:merge を迂回させない)  deny
// 対象はパスの repos/<owner>/<repo>(ほかのパスは対象なし)。

// apiValueFlags は値を取る gh api のフラグ。apiBoolFlags は値を取らないもの。ほかのフラグは断る。
var apiValueFlags = map[string]bool{"-X": true, "--method": true, "-f": true, "--raw-field": true, "-F": true, "--field": true,
	"--input": true, "-H": true, "--header": true, "-p": true, "--preview": true, "-q": true, "--jq": true,
	"-t": true, "--template": true, "--cache": true}
var apiBoolFlags = map[string]bool{"--paginate": true, "--slurp": true, "-i": true, "--include": true, "--silent": true, "--verbose": true}

// apiCall は gh api の呼び方。
type apiCall struct {
	method  string // 大文字。-X が無ければ、フィールドか --input があれば POST、無ければ GET(gh と同じ)
	path    string // 先頭の / と ?以降を外したもの(小文字にはしない)
	files   bool   // ローカルのファイルや標準入力を送る(--input、-F k=@file)
	decoded string // path の %エンコードを戻したもの(操作の分け方はこちらで見る)
}

// badHeader は通さないヘッダ(小文字)。
var badHeader = map[string]bool{"authorization": true, "host": true, "x-http-method-override": true, "x-http-method": true,
	"x-method-override": true}

// parseAPI は gh api の引数(args[0] == "api")を読む。読めなければ problem。
func parseAPI(args []string) (c apiCall, problem string) {
	fields, path := false, ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := a, "", false
		switch {
		case a == "--":
			return c, "gh api の -- は扱いません"
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			name, val, _ = strings.Cut(a, "=")
			hasVal = true
		case len(a) > 2 && a[0] == '-' && a[1] != '-':
			if apiValueFlags[a[:2]] {
				name, val, hasVal = a[:2], a[2:], true
			}
		}
		if !strings.HasPrefix(a, "-") {
			if path != "" {
				return c, "gh api のパスは 1 つだけ: " + a
			}
			path = a
			continue
		}
		switch {
		case apiBoolFlags[name] && !hasVal:
			continue
		case !apiValueFlags[name]:
			return c, "gh api の " + name + " は扱いません(--hostname など、別のホストや宛先を変えるものは通さない)"
		}
		if !hasVal {
			if i+1 >= len(args) {
				return c, "gh api の " + name + " に値が要ります"
			}
			i++
			val = args[i]
		}
		switch name {
		case "-X", "--method":
			c.method = strings.ToUpper(val)
		case "-f", "--raw-field":
			fields = true
		case "-F", "--field":
			fields = true
			if _, v, _ := strings.Cut(val, "="); strings.HasPrefix(v, "@") {
				c.files = true
			}
		case "--input":
			c.files = true
		case "-H", "--header":
			h, _, _ := strings.Cut(val, ":")
			if badHeader[strings.ToLower(strings.TrimSpace(h))] {
				return c, "gh api の -H " + strings.TrimSpace(h) + " は扱いません(認証・宛先・メソッドを変えるヘッダは通さない)"
			}
		}
	}
	if path == "" {
		return c, "gh api のパスが要ります(例: tb gh api repos/<owner>/<repo>/milestones)"
	}
	if strings.Contains(path, "://") || strings.Contains(path, "{") || strings.Contains(path, "..") ||
		strings.Contains(strings.ToLower(path), "%2e") || strings.Contains(path, "//") {
		return c, "gh api のパスは repos/<owner>/<repo>/... のような API のパスだけ(URL・{owner} などの穴埋め・.. は通さない): " + path
	}
	p, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "?")
	p, _, _ = strings.Cut(p, "#")
	c.path = strings.TrimSuffix(p, "/")
	dec, err := url.PathUnescape(c.path)
	if err != nil || strings.Contains(dec, "..") || strings.Contains(dec, "//") || strings.ContainsAny(dec, "?#\\") {
		return c, "gh api のパスを読めません(%エンコードで .. や / を隠したものは通さない): " + path
	}
	c.decoded = dec
	if c.method == "" {
		c.method = "GET"
		if fields || c.files {
			c.method = "POST"
		}
	}
	if !reMethod.MatchString(c.method) {
		return c, "gh api の -X は GET / POST / PATCH / PUT / DELETE: " + c.method
	}
	return c, ""
}

var reMethod = regexp.MustCompile(`^(GET|POST|PATCH|PUT|DELETE)$`)

// apiAdminSeg は呼び方を問わず、apiAdminWriteSeg は GET 以外のとき gh:api-admin にするパスの語(小文字、/ 区切り)。
var apiAdminSeg = map[string]bool{"secrets": true, "variables": true, "keys": true, "gpg_keys": true, "ssh_signing_keys": true,
	"hooks": true, "runners": true, "runner-groups": true, "authorizations": true, "installations": true, "installation": true,
	"oidc": true}
var apiAdminWriteSeg = map[string]bool{"collaborators": true, "invitations": true, "permissions": true, "protection": true,
	"rulesets": true, "environments": true, "transfer": true, "merge": true, "merges": true, "members": true, "memberships": true}

var reAPIReviews = regexp.MustCompile(`^repos/[^/]+/[^/]+/pulls/[^/]+/reviews(/[^/]+/events)?$`)
var reAPIRepoOnly = regexp.MustCompile(`^repos/[^/]+/[^/]+$`)

// apiOp は gh api の呼び方の操作名。
func apiOp(c apiCall) string {
	p := strings.ToLower(c.decoded)
	for _, seg := range strings.Split(p, "/") {
		if apiAdminSeg[seg] || c.method != "GET" && apiAdminWriteSeg[seg] {
			return "gh:api-admin"
		}
	}
	if p == "app" || strings.HasPrefix(p, "app/") || strings.HasPrefix(p, "applications/") {
		return "gh:api-admin"
	}
	if c.method != "GET" && (reAPIReviews.MatchString(p) || reAPIRepoOnly.MatchString(p)) {
		return "gh:api-admin" // approve(レビューの event)・リポジトリの設定の変更と削除
	}
	switch {
	case p == "graphql":
		return "gh:api-graphql"
	case c.method == "GET" && !c.files:
		return "gh:api-get"
	}
	return "gh:api-write"
}

// apiTarget はパスの repos/<owner>/<repo>(無ければ "")。
func apiTarget(c apiCall) string {
	if m := reAPIRepo.FindStringSubmatch(c.path); m != nil && ValidRepo(m[1]) {
		return m[1]
	}
	return ""
}
