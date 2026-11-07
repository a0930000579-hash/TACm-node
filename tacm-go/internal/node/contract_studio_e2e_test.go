package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/governance"
	"tacm/internal/vm"
	"tacm/internal/wallet"
)

// 本檔驗證 M44 代幣發行閉環（經 RPC 層）：
//  1. POST /contract/deploy 節點金鑰代簽發行標準代幣（name/symbol/supply）
//  2. 交易入塊後合約真實部署（code 寫入）
//  3. GET /contract/list 回傳 supply/name/symbol
//  4. GET /contract/call/{addr} 只讀模擬查詢（totalSupply/balanceOf/name/symbol）
//  5. POST /contract/call 節點代簽鏈上 transfer，入塊後餘額變動

// postJSON 對測試 server 發送 JSON POST。
func studioPostJSON(t *testing.T, ts *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s 失敗: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析 %s 響應失敗: %v", path, err)
	}
	return out
}

// getJSON 對測試 server 發送 GET。
func studioGetJSON(t *testing.T, ts *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s 失敗: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析 %s 響應失敗: %v", path, err)
	}
	return out
}

// contractStudioSetup 建立帶 RPC server 的節點。
func contractStudioSetup(t *testing.T) (*Node, *httptest.Server, string) {
	t.Helper()
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	insertGenesisWithAlloc(t, n, alice)
	// 節點金鑰地址（代簽發行者）。
	admin, err := n.keypair.Address()
	if err != nil {
		t.Fatal(err)
	}
	n.Start()
	// 等待創世分配同步進錢包帳本（代幣發行需 100 TACm），避免 Start() 後立即部署的啟動競態。
	waitFor(t, func() bool {
		acc, berr := n.Wallet().Balance(admin)
		if berr != nil || acc == nil {
			return false
		}
		return acc.TACmBalance.Cmp(big.NewInt(100)) >= 0
	}, 10*time.Second, "創世分配同步進錢包帳本")
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(func() {
		srv.Close()
		_ = n.Close()
	})
	return n, srv, admin
}

func TestContractStudioDeployAndQuery(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 1. 發行代幣。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "MyToken", "symbol": "TAC", "supply": "1000000", "from": admin,
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	txHash := out["tx_hash"].(string)
	t.Logf("deploy tx=%s contract=%s admin=%s", txHash, contractAddr, admin)

	// 2. 等待合約入塊（真實部署）。
	contract0x, err := tx0To0x(contractAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// 3. 列表帶元資料。
	info := studioGetJSON(t, srv, "/contract/list")
	contracts, _ := info["contracts"].([]any)
	found := false
	for _, c := range contracts {
		m := c.(map[string]any)
		if m["address"] == contractAddr {
			found = true
			if m["supply"] != "1000000" {
				t.Fatalf("supply 應為 1000000，得到 %v", m["supply"])
			}
			if m["name"] != "MyToken" {
				t.Fatalf("name 應為 MyToken，得到 %v", m["name"])
			}
			if m["symbol"] != "TAC" {
				t.Fatalf("symbol 應為 TAC，得到 %v", m["symbol"])
			}
		}
	}
	if !found {
		t.Fatal("列表未包含新合約")
	}

	// 4. 只讀模擬查詢。
	supply := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?calldata="+vmHex(vm.Erc20TotalSupplyCalldata()))
	if supply["ok"] != true || supply["return_data"] != "00000000000000000000000000000000000000000000000000000000000f4240" {
		t.Fatalf("totalSupply 查詢異常: %v", supply)
	}
	name := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?calldata="+vmHex(vm.Erc20NameCalldata()))
	if name["ok"] != true || !strings.Contains(name["return_data"].(string), "4d79546f6b656e") {
		t.Fatalf("name 查詢異常: %v", name)
	}
	symbol := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?calldata="+vmHex(vm.Erc20SymbolCalldata()))
	if symbol["ok"] != true || !strings.Contains(symbol["return_data"].(string), "544143") {
		t.Fatalf("symbol 查詢異常: %v", symbol)
	}
	// 節點金鑰餘額應等於 totalSupply（creator=admin）。
	bal := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?from="+admin+"&calldata="+vmHex(vm.Erc20BalanceOfCalldata(admin)))
	if bal["ok"] != true || bal["return_data"] != "00000000000000000000000000000000000000000000000000000000000f4240" {
		t.Fatalf("admin 餘額查詢異常: %v", bal)
	}
}

func TestContractStudioTransferOnChain(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 發行 1000000 TAC。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "TAC Gold", "symbol": "TG", "supply": "1000000", "from": admin,
	})
	contractAddr := out["contract_address"].(string)
	contract0x, _ := tx0To0x(contractAddr)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// 目標地址（無初始餘額）。注意：calldata 內地址必須為 0x 格式（addrToInt 僅解 0x）。
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	alice0x, _ := tx0To0x(alice)
	admin0x, _ := tx0To0x(admin)

	// 節點代簽 transfer(admin → alice 250)。
	calldata := vm.Erc20TransferCalldata(alice0x, big.NewInt(250))
	callOut := studioPostJSON(t, srv, "/contract/call", map[string]any{
		"to": contractAddr, "calldata": vmHex(calldata),
	})
	if callOut["ok"] != true {
		t.Fatalf("呼叫交易被拒: %v", callOut)
	}
	t.Logf("transfer tx=%s admin→%s 250", callOut["tx_hash"], alice)

	// 等待 transfer 入塊執行。
	waitFor(t, func() bool {
		bal := n.contracts.StorageAt(contract0x, vmAddrInt(alice0x))
		return bal.Cmp(big.NewInt(250)) == 0
	}, 8*time.Second, "transfer 未入塊")

	// 鏈上驗證。
	bal := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?from="+admin+"&calldata="+vmHex(vm.Erc20BalanceOfCalldata(alice0x)))
	if bal["ok"] != true || bal["return_data"] != "00000000000000000000000000000000000000000000000000000000000000fa" {
		t.Fatalf("alice 餘額應為 250(0xfa)，得到 %v", bal)
	}
	balAdmin := studioGetJSON(t, srv, "/contract/call/"+contractAddr+"?from="+admin+"&calldata="+vmHex(vm.Erc20BalanceOfCalldata(admin0x)))
	if balAdmin["ok"] != true || balAdmin["return_data"] != "00000000000000000000000000000000000000000000000000000000000f4146" {
		t.Fatalf("admin 餘額應為 999750(0xf4146)，得到 %v", balAdmin)
	}
}

// vmHex 包裝 hex 編碼（vm 套件內函數）。
func vmHex(b []byte) string { return hex.EncodeToString(b) }

// vmAddrInt 地址 → storage key（與 vm.addrToInt 一致：hex 解碼）。
func vmAddrInt(addr string) *big.Int {
	s := addr
	if strings.HasPrefix(s, "tx0") {
		// tx0 格式：轉 0x 後 hex 解碼（測試用簡化：取後 40 hex）
		s = strings.TrimPrefix(s, "tx0")
	}
	s = strings.TrimPrefix(s, "0x")
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

var _ = fmt.Sprintf

// TestContractStudioERC20Convenience 驗證便利端點：詳情查詢＋代簽轉帳（前端免組 calldata）。
func TestContractStudioERC20Convenience(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "Convenience", "symbol": "CV", "supply": "5000", "from": admin,
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	contract0x, _ := tx0To0x(contractAddr)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	// 詳情查詢（holder=admin）——supply/name/symbol/balance。
	info := studioGetJSON(t, srv, "/contract/erc20/"+contractAddr+"?holder="+admin)
	if info["ok"] != true || info["supply"] != "5000" || info["name"] != "Convenience" || info["symbol"] != "CV" || info["balance"] != "5000" {
		t.Fatalf("詳情查詢異常: %v", info)
	}

	// 便利轉帳（admin → alice 77）。
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	tr := studioPostJSON(t, srv, "/contract/erc20/transfer", map[string]any{
		"contract": contractAddr, "to": alice, "amount": "77",
	})
	if tr["ok"] != true {
		t.Fatalf("便利轉帳被拒: %v", tr)
	}
	alice0x, _ := tx0To0x(alice)
	waitFor(t, func() bool {
		bal := n.contracts.StorageAt(contract0x, vmAddrInt(alice0x))
		return bal.Cmp(big.NewInt(77)) == 0
	}, 8*time.Second, "便利轉帳未入塊")

	info2 := studioGetJSON(t, srv, "/contract/erc20/"+contractAddr+"?holder="+alice)
	if info2["balance"] != "77" {
		t.Fatalf("alice 餘額應為 77，得到 %v", info2["balance"])
	}
	info3 := studioGetJSON(t, srv, "/contract/erc20/"+contractAddr+"?holder="+admin)
	if info3["balance"] != "4923" {
		t.Fatalf("admin 餘額應為 4923，得到 %v", info3["balance"])
	}
}

// TestContractStudioGasLimit 驗證 M48 RPC gas_limit：正常值入塊、過小被模擬拒絕。
func TestContractStudioGasLimit(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)
	defer func() { _ = n.Close() }()

	// 1. gas_limit=5000000 正常部署入塊。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "GasCoin", "symbol": "GAS", "supply": "500000", "from": admin,
		"gas_limit": 5000000,
	})
	if out["ok"] != true {
		t.Fatalf("gas_limit=5M 部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	waitFor(t, func() bool {
		info := n.contracts.Get(must0x(t, contractAddr))
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "gas_limit 部署未入塊")

	// 2. gas_limit=100 過小：入池前模擬 OOG，RPC 應回 400。
	raw, err := json.Marshal(map[string]any{
		"name": "BadCoin", "symbol": "BAD", "supply": "1000",
		"gas_limit": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/contract/deploy", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("gas_limit=100 應被拒絕(400)，得到 %d", resp.StatusCode)
	}

	// 3. 過大 gas_limit（>10M）應被拒絕。
	resp2, err := http.Post(srv.URL+"/contract/deploy", "application/json",
		bytes.NewReader([]byte(`{"name":"X","symbol":"X","supply":"1000","gas_limit":99999999}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("gas_limit=99M 應被拒絕(400)，得到 %d", resp2.StatusCode)
	}
}

func must0x(t *testing.T, tx0 string) string {
	t.Helper()
	evm, err := tx0To0x(tx0)
	if err != nil {
		t.Fatal(err)
	}
	return evm
}

// M151c：treasury 出塊撥款 e2e——提案通過→到期→出塊引擎自獎勵池撥款→收款人入帳。
func TestGovernanceTreasuryPayout(t *testing.T) {
	n, _, admin := contractStudioSetup(t)
	// M151c-v2：治理金庫注資 100 TACm（模擬手續費預留累積）。
	if err := n.walletSvc.Deposit(wallet.TreasuryAddr, wallet.AssetTACm, wallet.NewAmountF(100, wallet.AssetTACm), "test topup"); err != nil {
		t.Fatal(err)
	}
	rpBefore, _ := n.walletSvc.Balance(wallet.RewardPoolAddr)
	// 投票窗口縮小至 2 塊以加速。
	if err := n.gov.SetConfig(governance.GovConfig{VotingWindow: 2, PassThreshold: 0.667}); err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	before, err := n.walletSvc.Balance(alice)
	if err != nil {
		t.Fatal(err)
	}
	h := n.DB().GetTipHeight()
	id, err := n.gov.Propose("test treasury payout", "reward pool -> alice",
		governance.TypeTreasury, alice, "30", admin, 100, h, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.gov.Vote(id, admin, governance.ChoiceYes, 100, h+1, ""); err != nil {
		t.Fatal(err)
	}
	// 等待撥款到帳（end=h+2；結算+撥款由出塊引擎處理，最多等 15s）。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if bal, _ := n.walletSvc.Balance(alice); bal.TACmBalance.String() != before.TACmBalance.String() {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	after, _ := n.walletSvc.Balance(alice)
	got, _ := strconv.ParseFloat(after.TACmBalance.String(), 64)
	want, _ := strconv.ParseFloat(before.TACmBalance.String(), 64)
	if got-want < 29.99 {
		t.Fatalf("撥款未到帳：before=%s after=%s", before.TACmBalance, after.TACmBalance)
	}
	// 提案狀態應為 executed。
	ps, _ := n.gov.List()
	found := false
	for _, p := range ps {
		if p.ID == id && p.Status == governance.StatusExecuted {
			found = true
		}
	}
	if !found {
		t.Fatal("treasury 提案應為 executed")
	}
	// 治理金庫應扣 30（100-30=70；期間無挹注入此池）。
	poolAfter, _ := n.walletSvc.Balance(wallet.TreasuryAddr)
	pa, _ := strconv.ParseFloat(poolAfter.TACmBalance.String(), 64)
	paT := pa / 1e18 // raw → TACM
	if paT < 69.9 || paT > 70.1 {
		t.Fatalf("治理金庫應約 70（100-30），得 %v", poolAfter.TACmBalance)
	}
	// 獎勵池不應被治理撥款動用（純做市用途；期間僅 coinbase 挹注，只增不減）。
	rpAfter, _ := n.walletSvc.Balance(wallet.RewardPoolAddr)
	if rpAfter.TACmBalance.Cmp(rpBefore.TACmBalance) < 0 {
		t.Fatalf("獎勵池不應被治理撥款動用（應只增不減）：before=%v after=%v", rpBefore.TACmBalance, rpAfter.TACmBalance)
	}
}


// M151c：RPC 層投票權重——權威帳本為 wallet store（錢包餘額 → 1 TACM = 1 票）。
// 修復：線上發現 tacmBalanceOf(DB accounts) 為 0，改由錢包服務餘額計權。
func TestGovernanceVoteViaRPC(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	// 注資錢包服務 100 TACm（權威帳本）。
	if err := n.walletSvc.Deposit(alice, wallet.AssetTACm, wallet.NewAmountF(100, wallet.AssetTACm), "test"); err != nil {
		t.Fatal(err)
	}
	if err := n.gov.SetConfig(governance.GovConfig{VotingWindow: 2, PassThreshold: 0.667}); err != nil {
		t.Fatal(err)
	}
	// 經 RPC 發起 meta 提案。
	propBody := fmt.Sprintf(`{"title":"rpc vote","desc":"","ptype":"meta","param_key":"","param_value":"","address":"%s"}`, alice)
	pr, err := http.Post(srv.URL+"/api/governance/propose", "application/json", strings.NewReader(propBody))
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Body.Close()
	var pd struct {
		OK    bool    `json:"ok"`
		Power float64 `json:"power"`
		ID    int64   `json:"proposal_id"`
	}
	if err := json.NewDecoder(pr.Body).Decode(&pd); err != nil {
		t.Fatal(err)
	}
	if !pd.OK || pd.ID <= 0 {
		t.Fatalf("提案應成功：%+v", pd)
	}
	if pd.Power < 99.9 || pd.Power > 100.1 {
		t.Fatalf("提案權重應為 100（錢包餘額），得 %v", pd.Power)
	}
	// 經 RPC 投票（錢包餘額權重，非 accounts 表）。
	voteBody := fmt.Sprintf(`{"proposal_id":%d,"choice":"yes","address":"%s"}`, pd.ID, alice)
	vr, err := http.Post(srv.URL+"/api/governance/vote", "application/json", strings.NewReader(voteBody))
	if err != nil {
		t.Fatal(err)
	}
	defer vr.Body.Close()
	var vd struct {
		OK    bool    `json:"ok"`
		Power float64 `json:"power"`
	}
	if err := json.NewDecoder(vr.Body).Decode(&vd); err != nil {
		t.Fatal(err)
	}
	if !vd.OK {
		t.Fatalf("投票應成功（錢包餘額權重）")
	}
	if vd.Power < 99.9 || vd.Power > 100.1 {
		t.Fatalf("投票權重應為 100，得 %v", vd.Power)
	}

// M151c-v2 死鎖回歸：param 提案通過→applyFn（內部重入 g.Config/SetConfig）不死鎖。
// 線上事故：TallyAndExecute 曾於持 g.mu.Lock() 下呼叫 applyGovernanceParam，
// 其內部再呼叫 g.Config()（RLock）與 g.SetConfig()（Lock）→ 同 goroutine 重入
// 互鎖 → 出塊引擎卡死、鏈停止出塊、proposals 端點掛起。重構為三階段
// （持鎖讀取→釋鎖執行→持鎖寫回）後，此測試必須在時限內完成且端點可查。
}
func TestGovernanceParamApplyNoDeadlock(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)
	// 注資提案人權重（錢包餘額＝投票權重）。
	if err := n.walletSvc.Deposit(admin, wallet.AssetTACm, wallet.NewAmountF(100, wallet.AssetTACm), "test power"); err != nil {
		t.Fatal(err)
	}
	if err := n.gov.SetConfig(governance.GovConfig{VotingWindow: 2, PassThreshold: 0.667}); err != nil {
		t.Fatal(err)
	}
	h := n.DB().GetTipHeight()
	id, err := n.gov.Propose("enable treasury fee", "30pct fee to treasury",
		governance.TypeParam, "treasury_fee_bps", "3000", admin, 100, h, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.gov.Vote(id, admin, governance.ChoiceYes, 100, h+1, ""); err != nil {
		t.Fatal(err)
	}
	// 出塊引擎自動 TallyAndExecute；若死鎖則提案永不 executed 且 List 掛起。
	deadline := time.Now().Add(15 * time.Second)
	executed := false
	for time.Now().Before(deadline) {
		ps, err := n.gov.List() // 死鎖時此呼叫掛起（RLock 等待被死鎖的 Lock）。
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.ID == id && p.Status == governance.StatusExecuted {
				executed = true
			}
		}
		if executed {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !executed {
		t.Fatal("param 提案應在投票窗口結束後 executed（疑似 TallyAndExecute 死鎖）")
	}
	// 參數應已熱更新（applyGovernanceParam 成功重入 g.SetConfig）。
	if got := n.gov.Config().TreasuryFeeBps; got != 3000 {
		t.Fatalf("treasury_fee_bps 應熱更新為 3000，得 %v", got)
	}
	// RPC 端點應可查（無掛起）。
	out := studioGetJSON(t, srv, "/api/governance/proposals")
	if out["ok"] != true {
		t.Fatalf("proposals 端點應正常回應：%v", out)
	}
	// 端點應反映新參數。
	out = studioGetJSON(t, srv, "/api/governance/params")
	if out["ok"] != true {
		t.Fatalf("params 端點應正常回應：%v", out)
	}
}

// M151c-v2：社群基金備援——治理金庫餘額不足時，由創世社群基金補足撥款。
func TestTreasuryCommunityFallback(t *testing.T) {
	n, _, admin := contractStudioSetup(t)
	pool, err := n.CommunityPoolAddress()
	if err != nil {
		t.Fatal(err)
	}
	// 社群基金注資 30 TACm；治理金庫不注資（餘額 0）。
	if err := n.walletSvc.Deposit(pool, wallet.AssetTACm, wallet.NewAmountF(30, wallet.AssetTACm), "community fund"); err != nil {
		t.Fatal(err)
	}
	if err := n.gov.SetConfig(governance.GovConfig{VotingWindow: 2, PassThreshold: 0.667}); err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	before, _ := n.walletSvc.Balance(alice)
	h := n.DB().GetTipHeight()
	id, err := n.gov.Propose("community fallback", "pool -> alice", governance.TypeTreasury, alice, "10", admin, 100, h, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.gov.Vote(id, admin, governance.ChoiceYes, 100, h+1, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if bal, _ := n.walletSvc.Balance(alice); bal.TACmBalance.String() != before.TACmBalance.String() {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	after, _ := n.walletSvc.Balance(alice)
	got, _ := strconv.ParseFloat(after.TACmBalance.String(), 64)
	want, _ := strconv.ParseFloat(before.TACmBalance.String(), 64)
	if got-want < 9.99 {
		t.Fatalf("社群基金撥款未到帳：before=%s after=%s", before.TACmBalance, after.TACmBalance)
	}
	// 社群基金應扣 10（30-10=20）。
	poolAfter, _ := n.walletSvc.Balance(pool)
	pf, _ := strconv.ParseFloat(poolAfter.TACmBalance.String(), 64)
	pfT := pf / 1e18
	if pfT < 19.9 || pfT > 20.1 {
		t.Fatalf("社群基金應約 20（30-10），得 %v", poolAfter.TACmBalance)
	}
}

// M151c-v2：治理金庫與社群基金皆不足時，提案保留 queued（下個區塊重試，不硬執行、不超額）。
func TestTreasuryInsufficientStaysQueued(t *testing.T) {
	n, _, admin := contractStudioSetup(t)
	// 治理金庫僅 5 TACm（不足以支付 10）。
	if err := n.walletSvc.Deposit(wallet.TreasuryAddr, wallet.AssetTACm, wallet.NewAmountF(5, wallet.AssetTACm), "low"); err != nil {
		t.Fatal(err)
	}
	if err := n.gov.SetConfig(governance.GovConfig{VotingWindow: 2, PassThreshold: 0.667}); err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	before, _ := n.walletSvc.Balance(alice)
	h := n.DB().GetTipHeight()
	id, err := n.gov.Propose("insufficient", "low -> alice", governance.TypeTreasury, alice, "10", admin, 100, h, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.gov.Vote(id, admin, governance.ChoiceYes, 100, h+1, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	after, _ := n.walletSvc.Balance(alice)
	if after.TACmBalance.String() != before.TACmBalance.String() {
		t.Fatalf("餘額不足不應撥款：before=%s after=%s", before.TACmBalance, after.TACmBalance)
	}
	ps, _ := n.gov.List()
	for _, p := range ps {
		if p.ID == id {
			if p.Status != governance.StatusQueued {
				t.Fatalf("餘額不足應保留 queued（下個區塊重試），得 %s", p.Status)
			}
			return
		}
	}
	t.Fatalf("提案不存在")
}
