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
	if _, err := g.Propose("", "d", TypeParam, "block_time", "5", "tx01AAA", 0, 1, ""); err == nil {
		t.Fatal("空標題應拒絕")
	}
	if _, err := g.Propose("t", "d", TypeParam, "block_time", "abc", "tx01AAA", 0, 1, ""); err == nil {
		t.Fatal("非數字參數值應拒絕")
	}
	if _, err := g.Propose("t", "d", TypeParam, "unknown_key", "5", "tx01AAA", 0, 1, ""); err == nil {
		t.Fatal("未知參數鍵應拒絕")
	}
	if _, err := g.Propose("t", "d", "bad_type", "", "", "tx01AAA", 0, 1, ""); err == nil {
		t.Fatal("未知提案類型應拒絕")
	}
}

func TestProposeAndVoteLifecycle(t *testing.T) {
	g := newTestGov(t)
	id, err := g.Propose("調整出塊時間", "將 block_time 調整為 3 秒", TypeParam, "block_time", "3", "tx01AAA", 0, 1, "hash1")
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

	id, err := g.Propose("調整難度", "將難度調為 3", TypeParam, "difficulty", "3", "tx01AAA", 0, 1, "")
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
	id, err := g.Propose("調整手續費", "提高手續費", TypeParam, "tx_fee_bps", "300", "tx01AAA", 0, 1, "")
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
	_, _ = g.Propose("提案一", "", TypeMeta, "", "", "tx01AAA", 0, 1, "")
	_, _ = g.Propose("提案二", "", TypeTreasury, "tx01CCC", "5", "tx01BBB", 0, 2, "")
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
	if len(ps) != 9 {
		t.Fatalf("應有 9 個治理參數（含 5 個 gov_*/treasury），實際 %d", len(ps))
	}
}

// M151：提案人最低權重門檻。
func TestProposerMinWeight(t *testing.T) {
	g := newTestGov(t)
	if err := g.SetConfig(GovConfig{ProposerMin: 10, TotalSupply: 100}); err != nil {
		t.Fatal(err)
	}
	// 權重 5 < 10 → 拒絕。
	if _, err := g.Propose("t", "d", TypeMeta, "", "", "tx01AAA", 5, 1, ""); err == nil {
		t.Fatal("權重不足應拒絕提案")
	}
	// 權重 15 ≥ 10 → 通過。
	if _, err := g.Propose("t", "d", TypeMeta, "", "", "tx01AAA", 15, 1, ""); err != nil {
		t.Fatalf("權重達標應可提案: %v", err)
	}
}

// M151：法定人數不足 → 拒絕。
func TestQuorumReject(t *testing.T) {
	g := newTestGov(t)
	if err := g.SetConfig(GovConfig{QuorumPct: 50, TotalSupply: 10, TimelockBlocks: 0}); err != nil {
		t.Fatal(err)
	}
	id, err := g.Propose("t", "d", TypeParam, "block_time", "3", "tx01AAA", 0, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// 總投票權重 4 < quorum 5。
	_ = g.Vote(id, "tx01B1", ChoiceYes, 1, 2, "")
	_ = g.Vote(id, "tx01B2", ChoiceYes, 1, 3, "")
	_ = g.Vote(id, "tx01B3", ChoiceYes, 1, 4, "")
	_ = g.Vote(id, "tx01B4", ChoiceYes, 1, 5, "")
	out := g.TallyAndExecute(21)
	if len(out) != 1 || out[0].Status != StatusRejected {
		t.Fatalf("法定人數不足應拒絕，實際 %v", out)
	}
}

// M151：時間鎖——通過後 queued，延遲屆滿才執行。
func TestTimelockQueueAndExecute(t *testing.T) {
	g := newTestGov(t)
	applied := ""
	g.SetApplyFunc(func(key, value string) error {
		applied = key + "=" + value
		return nil
	})
	if err := g.SetConfig(GovConfig{TimelockBlocks: 10}); err != nil {
		t.Fatal(err)
	}
	id, err := g.Propose("調整難度", "d", TypeParam, "difficulty", "3", "tx01AAA", 0, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = g.Vote(id, "tx01B1", ChoiceYes, 1, 2, "")
	_ = g.Vote(id, "tx01B2", ChoiceYes, 1, 3, "")
	_ = g.Vote(id, "tx01B3", ChoiceYes, 1, 4, "")
	_ = g.Vote(id, "tx01B4", ChoiceNo, 1, 5, "")
	// 到期（end=21）→ queued（時間鎖 10 區塊）。
	out := g.TallyAndExecute(21)
	if len(out) != 1 || out[0].Status != StatusQueued {
		t.Fatalf("時間鎖未到應 queued，實際 %v", out)
	}
	if applied != "" {
		t.Fatalf("時間鎖未到不應執行參數，實際 %q", applied)
	}
	// 延遲未滿（end+9=30）不執行。
	if out := g.ExecuteDue(30); len(out) != 0 {
		t.Fatalf("延遲未滿不應執行，實際 %v", out)
	}
	// 延遲屆滿（end+10=31）執行。
	out = g.ExecuteDue(31)
	if len(out) != 1 || out[0].Status != StatusExecuted {
		t.Fatalf("延遲屆滿應執行，實際 %v", out)
	}
	if applied != "difficulty=3" {
		t.Fatalf("應執行 difficulty=3，實際 %q", applied)
	}
}

// M151：Config 設定/回讀（含 0 值欄位維持現值）。
func TestConfigRoundtrip(t *testing.T) {
	g := newTestGov(t)
	if err := g.SetConfig(GovConfig{QuorumPct: 30, TotalSupply: 500, TimelockBlocks: 5, ProposerMin: 2}); err != nil {
		t.Fatal(err)
	}
	c := g.Config()
	if c.QuorumPct != 30 || c.TotalSupply != 500 || c.TimelockBlocks != 5 || c.ProposerMin != 2 {
		t.Fatalf("Config 回讀不符: %+v", c)
	}
	if c.VotingWindow <= 0 || c.PassThreshold <= 0 {
		t.Fatalf("預設值不應被清空: %+v", c)
	}
}

// M151a：gov_* 治理參數驗證（0=關閉 合法，超範圍拒絕）。
func TestGovParamValidation(t *testing.T) {
	g := newTestGov(t)
	// 合法（含 0＝關閉）。
	for _, kv := range [][2]string{
		{"gov_quorum_pct", "0"}, {"gov_quorum_pct", "50"},
		{"gov_timelock_blocks", "0"}, {"gov_timelock_blocks", "100"},
		{"gov_proposer_min", "0"}, {"gov_proposer_min", "5"},
		{"gov_total_supply", "0"}, {"gov_total_supply", "1000000"},
	} {
		if _, err := g.Propose("t", "d", TypeParam, kv[0], kv[1], "tx01AAA", 0, 1, ""); err != nil {
			t.Fatalf("%s=%s 應合法: %v", kv[0], kv[1], err)
		}
	}
	// 非法：超範圍／負值。
	for _, kv := range [][2]string{
		{"gov_quorum_pct", "101"}, {"gov_quorum_pct", "-1"},
		{"gov_timelock_blocks", "-5"}, {"gov_timelock_blocks", "2000000"},
		{"gov_proposer_min", "101"},
		{"gov_total_supply", "-1"},
		{"gov_unknown", "10"},
	} {
		if _, err := g.Propose("t", "d", TypeParam, kv[0], kv[1], "tx01AAA", 0, 1, ""); err == nil {
			t.Fatalf("%s=%s 應拒絕", kv[0], kv[1])
		}
	}
}

// M151a：Params() 的 gov_* 參數動態反映 GovConfig 現值。
func TestGovParamsDynamicValue(t *testing.T) {
	g := newTestGov(t)
	if err := g.SetConfig(GovConfig{QuorumPct: 30, TimelockBlocks: 7, ProposerMin: 2, TotalSupply: 500}); err != nil {
		t.Fatal(err)
	}
	ps := g.Params()
	vals := make(map[string]string)
	for _, p := range ps {
		vals[p.Key] = p.Value
	}
	if vals["gov_quorum_pct"] != "30" || vals["gov_timelock_blocks"] != "7" ||
		vals["gov_proposer_min"] != "2" || vals["gov_total_supply"] != "500" {
		t.Fatalf("gov_* 參數現值未反映 config: %v", vals)
	}
	if vals["block_time"] == "" {
		t.Fatal("既有參數不應被清空")
	}
}

// M151a：Apply() 串接 gov_* 參數（模擬 node 層 applyGovernanceParam 回調）。
func TestGovParamApply(t *testing.T) {
	g := newTestGov(t)
	applied := ""
	g.SetApplyFunc(func(key, value string) error {
		applied = key + "=" + value
		return nil
	})
	if err := g.Apply("gov_quorum_pct", "40"); err != nil {
		t.Fatal(err)
	}
	if applied != "gov_quorum_pct=40" {
		t.Fatalf("applyFn 未收到 gov_ 參數: %q", applied)
	}
}

// M151c：treasury 提案——驗證＋queued＋到期撥款清單＋標記 executed。
func TestTreasuryProposalAndExecution(t *testing.T) {
	g := newTestGov(t)
	// 驗證：無收款地址拒絕
	if _, err := g.Propose("t", "d", TypeTreasury, "", "5", "tx01AAA", 0, 1, ""); err == nil {
		t.Fatal("treasury 無收款地址應拒絕")
	}
	// 驗證：金額非正數拒絕
	if _, err := g.Propose("t", "d", TypeTreasury, "tx01BBB", "0", "tx01AAA", 0, 1, ""); err == nil {
		t.Fatal("treasury 金額 0 應拒絕")
	}
	id, err := g.Propose("國庫撥款", "獎勵池 → 開發基金", TypeTreasury, "tx01BBB", "12.5", "tx01AAA", 0, 1, "hashT1")
	if err != nil {
		t.Fatal(err)
	}
	// 投票通過（提案人 100 權重）
	if err := g.Vote(id, "tx01AAA", ChoiceYes, 100, 2, "vh1"); err != nil {
		t.Fatal(err)
	}
	settled := g.TallyAndExecute(21) // end=1+20=21 到期
	if len(settled) != 1 {
		t.Fatalf("應結算 1 筆，得 %d", len(settled))
	}
	// 通過後一律 queued（即使 timelock=0）：由出塊引擎撥款後才 executed
	if settled[0].Status != StatusQueued {
		t.Fatalf("treasury 通過後應 queued，得 %s", settled[0].Status)
	}
	// 到期前無待撥款
	pend, err := g.PendingTreasury(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 0 {
		t.Fatalf("到期前不應有待撥款，得 %d", len(pend))
	}
	// 到期後有待撥款
	pend, err = g.PendingTreasury(23) // end=21(1+20) + timelock 0 ≤ 23
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].ProposalID != id || pend[0].To != "tx01BBB" || pend[0].Amount != "12.5" {
		t.Fatalf("待撥款不符: %+v", pend)
	}
	// ExecuteDue 不應吃掉 treasury（保留 queued）
	if ex := g.ExecuteDue(23); len(ex) != 0 {
		t.Fatalf("ExecuteDue 不應處理 treasury，得 %+v", ex)
	}
	// 撥款後標記 executed
	if err := g.MarkTreasuryExecuted(id); err != nil {
		t.Fatal(err)
	}
	pend, _ = g.PendingTreasury(23)
	if len(pend) != 0 {
		t.Fatalf("executed 後不應再有待撥款，得 %d", len(pend))
	}
}
