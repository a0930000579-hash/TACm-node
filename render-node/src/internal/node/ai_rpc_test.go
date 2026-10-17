package node

// M73-A AI 服務層測試：意圖匹配 + 鏈上回答（中英雙語）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testAlice = "tx0alice0000000000000000000000000000"

func newAIHost(t *testing.T) *RPCServer {
	t.Helper()
	n, err := New(testCfg(t), "ai1", 3)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	insertGenesisWithAlloc(t, n, testAlice)
	return NewRPCServer(n)
}

func TestAIIntentMatch(t *testing.T) {
	cases := []struct {
		q    string
		want aiIntent
	}{
		{"node status", aiStatus},
		{"節點狀態", aiStatus},
		{"最新區塊", aiBlock},
		{"block 5 的資訊", aiBlock},
		{"鏈上審計", aiAudit},
		{"audit consistent", aiAudit},
		{"查詢地址 tx0xxx 的餘額", aiAddress},
		{"獎勵怎麼分", aiReward},
		{"在線礦工", aiMiner},
		{"總供應多少", aiSupply},
		{"加入節點", aiJoin},
		{"轉帳手續費", aiTransfer},
		{"hello", aiHelp},
	}
	for _, c := range cases {
		if got := matchAIIntent(c.q); got != c.want {
			t.Errorf("matchAIIntent(%q) = %q, want %q", c.q, got, c.want)
		}
	}
}

func aiAsk(t *testing.T, s *RPCServer, q, lang string) map[string]any {
	t.Helper()
	url := "/api/ai/ask?q=" + url.QueryEscape(q)
	if lang != "" {
		url += "&lang=" + lang
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask %q status = %d: %s", q, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("ask %q 解析失敗: %v", q, err)
	}
	if out["ok"] != true {
		t.Fatalf("ask %q ok=false: %s", q, rec.Body.String())
	}
	return out
}

func TestAIAssistantAnswers(t *testing.T) {
	s := newAIHost(t)
	cases := []struct {
		q, lang, contains string
	}{
		{"node status", "en", "Block height"},
		{"節點狀態", "zh", "區塊高度"},
		{"最新區塊", "zh", "區塊 #"},
		{"block 0", "en", "Block #0"},
		{"鏈上審計", "zh", "審計"},
		{"audit", "en", "consistent"},
		{"查詢地址 " + testAlice + " 的餘額", "zh", "1000"},
		{"balance of " + testAlice, "en", "1000"},
		{"獎勵怎麼分", "zh", "16%"},
		{"rewards", "en", "M70"},
		{"在線礦工", "zh", "礦工"},
		{"總供應", "zh", "供應"},
		{"加入節點", "zh", "節點"},
		{"轉帳手續費", "zh", "手續費"},
		{"hello", "en", "assistant"},
	}
	for _, c := range cases {
		out := aiAsk(t, s, c.q, c.lang)
		ans, _ := out["answer"].(string)
		if !strings.Contains(ans, c.contains) {
			t.Errorf("ask(%q,%q) 回答不含 %q：%s", c.q, c.lang, c.contains, ans)
		}
		if out["lang"] != c.lang {
			t.Errorf("ask(%q) lang = %v, want %s", c.q, out["lang"], c.lang)
		}
	}
}

func TestAIAssistantMissingQ(t *testing.T) {
	s := newAIHost(t)
	req := httptest.NewRequest(http.MethodGet, "/api/ai/ask", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 q 應 400，實際 %d", rec.Code)
	}
}

func TestAIAssistantBlockHeightParsing(t *testing.T) {
	// 創世後出一塊（高度 1），驗證「最新區塊」回 tip。
	n, err := New(testCfg(t), "ai2", 1)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	insertGenesis(t, n)
	if err := n.produceBlock(); err != nil {
		t.Fatalf("出塊失敗: %v", err)
	}
	s := NewRPCServer(n)
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 1 }, 10*time.Second, "鏈高未到 1")
	out := aiAsk(t, s, "最新區塊", "en")
	ans, _ := out["answer"].(string)
	if !strings.Contains(ans, "Block #1") {
		t.Errorf("最新區塊回答異常：%s", ans)
	}
}
