package tickets

import (
	"slices"
	"testing"
)

func TestParseStatuses(t *testing.T) {
	names, cats, ok := ParseStatuses("# 例\nbacklog todo = 未着手\ndoing active\nshipped done  # 完了\n\nqa review\n")
	if !ok || !slices.Equal(names, []string{"backlog", "doing", "shipped", "qa"}) || cats["qa"] != "review" {
		t.Fatalf("ParseStatuses = %v %v %v", names, cats, ok)
	}
	for _, bad := range []string{
		"",                             // 状態が無い
		"a todo\nc done\n",             // active が無い
		"a todo\nb doing\nc done\n",    // 知らない分類
		"a todo\na active\nc done\n",   // 重複
		"a* todo\nb active\nc done\n",  // glob の文字
		"a todo x\nb active\nc done\n", // 3 語
		"a'b todo\nb active\nc done\n", // 引用符
	} {
		if _, _, ok := ParseStatuses(bad); ok {
			t.Errorf("ParseStatuses(%q) ok; want refused", bad)
		}
	}
}

func TestStatusSetIn(t *testing.T) {
	names, cats, _ := ParseStatuses("backlog todo\ndoing active\nshipped done\ndropped cancelled\n")
	s := StatusSet{Names: names, cat: cats}
	for _, b := range builtin { // 組み込みの名前は設定に無くても組み込みの分類で数える
		if _, ok := s.cat[b[0]]; !ok {
			s.cat[b[0]] = b[1]
		}
	}
	if got := s.In("done", "cancelled"); !slices.Equal(got, []string{"shipped", "dropped", "done", "cancelled"}) {
		t.Errorf("In(done, cancelled) = %v", got)
	}
	if got := s.SQLList("active"); got != "'doing','in_progress'" {
		t.Errorf("SQLList(active) = %s", got)
	}
	if got := (StatusSet{cat: map[string]string{}}).SQLList("review"); got != "NULL" {
		t.Errorf("SQLList on empty = %s", got)
	}
}
