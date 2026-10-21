package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"tacm/internal/chaindb"
)

// mkBlockDetail 構造 /api/block/{height} 回應：平鋪區塊欄位 + coinbase 瓜分交易。
// splits 指定礦工瓜分（Address→Amount），無礦工時 88% 進 reserve（與節點語義一致）。
func mkBlockDetail(h int64, ts int64, diff int, splits []struct {
	Addr   string
	Amount float64
}, noMiner bool) *blockDetailResp {
	reward := chaindb.BlockReward(h)
	pool := reward * float64(chaindb.PoolShareBps) / 10000
	rest := reward - pool
	bd := &blockDetailResp{
		Height: h, Ts: ts, Difficulty: diff,
		Hash: "h" + strconv.FormatInt(h, 10), PrevHash: "p" + strconv.FormatInt(h-1, 10),
	}
	add := func(to, memo string, amt float64) {
		if amt <= 1e-9 {
			return
		}
		bd.Transactions = append(bd.Transactions, txResp{TxHash: "tx" + strconv.Itoa(len(bd.Transactions)), ToAddr: to, Amount: chaindb.FormatFloat(amt), Memo: memo})
	}
	if h == 0 {
		return bd // 創世塊無 coinbase
	}
	if noMiner {
		add(chaindb.RewardPoolAddr, "coinbase:reserve", rest)
		add(chaindb.RewardPoolAddr, "coinbase:pool", pool)
		return bd
	}
	for _, sp := range splits {
		add(sp.Addr, "coinbase:miner", sp.Amount)
	}
	add(chaindb.RewardPoolAddr, "coinbase:pool", pool)
	return bd
}

// mockNode 建立模擬節點 RPC：/status 固定 tip，/block/{height} 回指定鏈。
func mockNode(t *testing.T, tip int64, blocks map[int64]*blockDetailResp) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodeStatusResp{NodeID: "mock", ChainID: "tacm-mainnet-1", BlockHeight: tip, EffectiveDifficulty: 1})
	})
	mux.HandleFunc("/api/block/", func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.URL.Path, "/api/block/")
		h, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "bad height", http.StatusBadRequest)
			return
		}
		bd, ok := blocks[h]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bd)
	})
	return httptest.NewServer(mux)
}

func TestAuditBlock_CoinbaseMatchesReward(t *testing.T) {
	// 有礦工瓜分：miner 總額必須 == 88% 獎勵，pool == 12%。
	h := int64(100)
	reward := chaindb.BlockReward(h)
	rest := reward * (1 - float64(chaindb.PoolShareBps)/10000)
	bd := mkBlockDetail(h, 1000, 2, []struct {
		Addr   string
		Amount float64
	}{{Addr: "tx0miner1", Amount: rest}}, false)
	ba := auditBlock(h, bd.Transactions)
	if !ba.CoinbaseOK || !ba.PoolOK || !ba.SplitOK {
		t.Fatalf("正常塊應全過：%+v issues=%v", ba, ba.Issues)
	}
	if ba.CoinbaseSum != reward {
		t.Errorf("coinbase 總額 %f != reward %f", ba.CoinbaseSum, reward)
	}
}

func TestAuditBlock_NoMinerReserve(t *testing.T) {
	// 無礦工：88% 進 reserve（仍屬 88% 份額），pool 12%。
	h := int64(50)
	bd := mkBlockDetail(h, 500, 2, nil, true)
	ba := auditBlock(h, bd.Transactions)
	if !ba.OK {
		t.Fatalf("無礦工塊應全過：%+v issues=%v", ba, ba.Issues)
	}
}

func TestAuditBlock_OverMintDetected(t *testing.T) {
	// 異常塊：coinbase 總額多出 1 TACm → 必須被抓出。
	h := int64(10)
	reward := chaindb.BlockReward(h)
	bd := mkBlockDetail(h, 100, 2, []struct {
		Addr   string
		Amount float64
	}{{Addr: "tx0miner1", Amount: reward + 1}}, false) // 多 1（88% 之外的超發）
	ba := auditBlock(h, bd.Transactions)
	if ba.CoinbaseOK {
		t.Errorf("超發塊應被判異常（sum=%f reward=%f）", ba.CoinbaseSum, reward)
	}
	if ba.SplitOK {
		t.Errorf("超發塊 miner 份額應異常（miner=%f 88%%=%f）", ba.MinerSum, reward*(1-float64(chaindb.PoolShareBps)/10000))
	}
}

func TestAuditChain_HealthyChain(t *testing.T) {
	// 3 塊健康鏈（創世 + 2 出塊），全部通過。
	blocks := map[int64]*blockDetailResp{
		0: mkBlockDetail(0, 0, 1, nil, false),
		1: mkBlockDetail(1, 100, 1, []struct {
			Addr   string
			Amount float64
		}{{Addr: "tx0miner1", Amount: chaindb.BlockReward(1) * (1 - float64(chaindb.PoolShareBps)/10000)}}, false),
		2: mkBlockDetail(2, 200, 1, nil, true),
	}
	ts := mockNode(t, 2, blocks)
	defer ts.Close()

	res, err := auditChainFetch(ts.URL)
	if err != nil {
		t.Fatalf("auditChain: %v", err)
	}
	if !res.Passed {
		t.Errorf("健康鏈應通過：issues=%v", res.Issues)
	}
	if res.BlockCount != 3 || res.CoinbasePass != 3 {
		t.Errorf("區塊數 %d / coinbase 通過 %d 異常", res.BlockCount, res.CoinbasePass)
	}
	want := chaindb.BlockReward(1) + chaindb.BlockReward(2)
	if res.TotalMinted != want {
		t.Errorf("累計增發 %f != 期望 %f", res.TotalMinted, want)
	}
}

func TestAuditChain_OverMintChainDetected(t *testing.T) {
	// 異常鏈：block 1 coinbase 超發 → audit 必須失敗並列出 issue。
	blocks := map[int64]*blockDetailResp{
		0: mkBlockDetail(0, 0, 1, nil, false),
		1: mkBlockDetail(1, 100, 1, []struct {
			Addr   string
			Amount float64
		}{{Addr: "tx0miner1", Amount: chaindb.BlockReward(1) * (1 - float64(chaindb.PoolShareBps)/10000)}}, false),
		2: mkBlockDetail(2, 200, 1, []struct {
			Addr   string
			Amount float64
		}{{Addr: "tx0bad", Amount: chaindb.BlockReward(2) + 5}}, false), // 超發 5
	}
	ts := mockNode(t, 2, blocks)
	defer ts.Close()

	res, err := auditChainFetch(ts.URL)
	if err != nil {
		t.Fatalf("auditChain: %v", err)
	}
	if res.Passed {
		t.Error("超發鏈不應通過")
	}
	if res.CoinbaseFail == 0 {
		t.Error("應偵測到 coinbase 異常")
	}
	found := false
	for _, is := range res.Issues {
		if strings.Contains(is, "h=2") && strings.Contains(is, "coinbase 總額") {
			found = true
		}
	}
	if !found {
		t.Errorf("應有 h=2 coinbase 超發 issue：%v", res.Issues)
	}
}

// auditChainFetch 以 mock node 執行全鏈審計（走真實 HTTP 拉取）。
func auditChainFetch(node string) (AuditResult, error) {
	client := &http.Client{}
	st, err := getNodeStatus(client, node)
	if err != nil {
		return AuditResult{}, err
	}
	return auditChain(st, func(h int64) (*blockDetailResp, error) {
		return getBlock(client, node, h)
	}), nil
}
