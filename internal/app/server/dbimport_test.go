package server

import (
	"path/filepath"
	"testing"

	"github.com/imohiyoko/API/define"

	"github.com/imohiyoko/herdrorchestrator/internal/app"
	appconfig "github.com/imohiyoko/herdrorchestrator/internal/app/config"
)

func TestConfigDBImportRefused(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	env, dbp := fixture(t, "")
	if _, e := callOp[appconfig.DBImportResult](t, env, app.Human(), "config.db.import", appconfig.DBImportReq{Path: "old.db"}); e == nil || e.Code != define.CodeInvalidArgument {
		t.Errorf("相対パス: %v", e)
	}
	// 既定では人だけ(commands.tbl の inner の行)
	if _, e := callOp[appconfig.DBImportResult](t, env, app.Agent("w1"), "config.db.import", appconfig.DBImportReq{Path: filepath.Join(t.TempDir(), "old.db")}); e == nil || e.Code != define.CodePermissionDenied {
		t.Errorf("agent: %v", e)
	}
	if _, e := callOp[appconfig.DBImportResult](t, env, app.Human(), "config.db.import", appconfig.DBImportReq{Path: dbp}); e == nil || e.Code != define.CodeFailedPrecondition {
		t.Errorf("取り込み先と同じ DB: %v", e)
	}
}
