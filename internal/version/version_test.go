package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestStringHasVersionAndPlatform(t *testing.T) {
	s := String()
	for _, want := range []string{"herdrctl dev", runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, want it to contain %q", s, want)
		}
	}
}
