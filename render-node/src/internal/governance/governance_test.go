package governance

import (
	"path/filepath"
	"testing"
)

// newTestGov 建立測試治理管理器。
func newTestGov(t *testing.T) *Governance {
	t.Helper()
	g, err := New(filepath.Join(t.TempDir(), "gov.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func TestProposeValidation(t *testing.T) {
	g := newTestGov(t)
	if _, err := g.Propose("", "d", TypeParam, "block_time", "5", "tx01AAA", 1, ""); err == nil {
		t.Fatal("空標題應拒絕")
	}
	if _, err := g.Propose("t", "d", TypeParam, "block_time", "abc", "tx01AAA", 1, ""); err == nil {
		t.Fatal("非數字參數值應拒絕")
	}
	if _, err := g.Propose("t", "d", TypeParam, "unknown_key", "5", "tx01AAA", 1, ""); err == nil {
		t.Fatal("未知參數鍵應拒絕")
	}
	if _, err := g.Propose("t", "d", "bad_type", "", "", "tx01AAA", 1, ""); err == nil {
		t.Fatal("未知提案類型應拒絕")
	}
}

func TestProposeAndVoteLifecycle(t *testing.T) {
	g := newTestGov(t)
	id, err := g.Propose("調整出塊時間", "將 block_time 調整為 3 秒", TypeParam, "block_time", "3", "tx01AAA", 1, "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("提案 ID 應為 1，實際 %d", id)
	}

	// 非法投票選擇。
	if err := g.Vote(id, "tx01BBB", "maybe", 1, 2, ""); err == nil {
		t.Fatal("未知投票選擇應拒絕")
	}
	// 正常投票。
	if err := g.Vote(id, "tx01BBB", ChoiceYes, 1, 2, "hash2"); err != nil {
		t.Fatal(err)
	}
	// 重複投票（同人）不報錯但僅記一次。
	if err := g.Vote(id, "tx01BBB", ChoiceNo, 1, 3, "hash3"); err != nil {
		t.Fatal(err)
	}
	votes, err := g.Votes(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 1 {
		t.Fatalf("同一投票人應僅記錄一次，實際 %d", len(votes))
	}

	// 對不存在提案投票。
	if err := g.Vote(999, "tx01CCC", ChoiceYes, 1, 2, ""); err == nil {
		t.Fatal("不存在的提案應拒絕")
	}
}

func TestTallyPassAndExecute(t *testing.T) {
	g := newTestGov(t)
	applied := ""
	g.SetApplyFunc(func(key, value string) error {
		applied = key + "=" + value
		return nil
	})

	id, err := g.Propose("調整難度", "將難度調為 3", TypeParam, "difficulty", "3", "tx01AAA", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// 投票：總權重 4，贊成 3（3/4=75% > 2/3 門檻）。
	_ = g.Vote(id, "tx01B1", ChoiceYes, 1, 2, "")
	_ = g.Vote(id, "tx01B2", ChoiceYes, 1, 3, "")
	_ = g.Vote(id, "tx01B3", ChoiceYes, 1, 4, "")
	_ = g.Vote(id, "tx01B4", ChoiceNo, 1, 5, "")

	// 到期前不結算。
	if out := g.TallyAndExecute(10); len(out) != 0 {
		t.Fatal("未到期不應結算")
	}
	// 到期（start=1，window=20，end=21）統計。
	out := g.TallyAndExecute(21)
	if len(out) != 1 {
		t.Fatalf("到期應結算 1 筆，實際 %d", len(out))
	}
	if out[0].Status != StatusExecuted {
		t.Fatalf("贊成達 2/3 應執行，實際 %s", out[0].Status)
	}
	if applied != "difficulty=3" {
		t.Fatalf("應執行 difficulty=3，實際 %q", applied)
	}
}

func TestTallyReject(t *testing.T) {
	g := newTestGov(t)
	id, err := g.Propose("調整手續費", "提高手續費", TypeParam, "tx_fee_bps", "300", "tx01AAA", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = g.Vote(id, "tx01B1", ChoiceNo, 1, 2, "")
	_ = g.Vote(id, "tx01B2", ChoiceNo, 1, 3, "")
	_ = g.Vote(id, "tx01B3", ChoiceYes, 1, 4, "")
	_ = g.Vote(id, "tx01B4", ChoiceYes, 1, 5, "")
	out := g.TallyAndExecute(21)
	if len(out) != 1 {
		t.Fatalf("到期應結算 1 筆，實際 %d", len(out))
	}
	if out[0].Status != StatusRejected {
		t.Fatalf("贊成未達 2/3 應拒絕，實際 %s", out[0].Status)
	}
}

func TestListAndParams(t *testing.T) {
	g := newTestGov(t)
	_, _ = g.Propose("提案一", "", TypeMeta, "", "", "tx01AAA", 1, "")
	_, _ = g.Propose("提案二", "", TypeTreasury, "", "", "tx01BBB", 2, "")
	list, err := g.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("應有 2 筆提案，實際 %d", len(list))
	}
	if list[0].Title != "提案二" {
		t.Fatalf("列表應新→舊排序，實際首筆 %q", list[0].Title)
	}
	ps := g.Params()
	if len(ps) != 4 {
		t.Fatalf("應有 4 個治理參數，實際 %d", len(ps))
	}
}
