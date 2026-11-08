package node

// agent_billing_file_test.go — TACm 計費與檔案生成/下載的單元與 HTTP 驗收。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tacm/internal/wallet"
)

func TestMeterAgentTurnChargesWhenFunded(t *testing.T) {
	s := newAIHost(t)
	if err := s.node.walletSvc.Deposit(testAlice, wallet.AssetTACm, wallet.NewAmountF(1000, wallet.AssetTACm), "test:topup"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	before := mustTACmBalance(t, s, testAlice)
	b, err := s.node.meterAgentTurn(testAlice, false, "rules", "low")
	if err != nil {
		t.Fatalf("meter: %v", err)
	}
	if !b.Charged || b.Reason != "assistant_turn" {
		t.Fatalf("expected charged assistant_turn, got %+v", b)
	}
	after := mustTACmBalance(t, s, testAlice)
	if before.Cmp(after) <= 0 {
		t.Fatalf("sender balance should decrease: before=%s after=%s", before, after)
	}
	fee := mustTACmBalance(t, s, titanFeeAccount)
	if fee.Sign() <= 0 {
		t.Fatalf("fee account should hold funds, got %s", fee)
	}
}

func TestMeterAgentTurnInsufficientGraceful(t *testing.T) {
	s := newAIHost(t)
	b, err := s.node.meterAgentTurn(testAlice, true, "rules", "low")
	if err != nil {
		t.Fatalf("graceful billing must not error: %v", err)
	}
	if b.Charged || b.Reason != "insufficient_balance" {
		t.Fatalf("expected skipped insufficient_balance, got %+v", b)
	}
}

func TestMeterAgentTurnNoAddress(t *testing.T) {
	s := newAIHost(t)
	b, err := s.node.meterAgentTurn("", false, "rules", "low")
	if err != nil {
		t.Fatalf("no-addr billing must not error: %v", err)
	}
	if b.Charged || b.Reason != "no_linked_address" {
		t.Fatalf("expected no_linked_address, got %+v", b)
	}
}

func TestCreateFileAndDownload(t *testing.T) {
	s := newAIHost(t)
	reg := s.buildAgentRegistry(nil, testAlice)
	tool, ok := reg.Get("create_file")
	if !ok {
		t.Fatal("create_file tool not registered")
	}
	content := "package main\nfunc main(){}\n"
	res, err := tool.Run(context.Background(), map[string]any{
		"filename": "hello", "language": "go", "content": content,
	})
	if err != nil {
		t.Fatalf("create_file: %v", err)
	}
	data, _ := res.Data.(map[string]any)
	url, _ := data["download_url"].(string)
	if !strings.HasPrefix(url, "/api/ai/file/") {
		t.Fatalf("bad download url: %v", data)
	}
	if got, _ := data["filename"].(string); got != "hello.go" {
		t.Fatalf("extension should be ensured, got %q", got)
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + url)
	if err != nil {
		t.Fatalf("GET file: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "hello.go") {
		t.Fatalf("missing attachment filename: %s", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != content {
		t.Fatalf("body mismatch: %q", string(body))
	}

	// 未知 id → 404。
	miss, err := http.Get(srv.URL + "/api/ai/file/af_doesnotexist")
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	defer miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", miss.StatusCode)
	}
}

// TestDifficultyMultiplier 驗證推理難度倍數：low ×1、medium ×1.5、high ×3。
func TestDifficultyMultiplier(t *testing.T) {
	if got := difficultyFeeMultiplier("low"); got != 1.0 {
		t.Fatalf("low 應為 1，實際 %v", got)
	}
	if got := difficultyFeeMultiplier("medium"); got != 1.5 {
		t.Fatalf("medium 應為 1.5，實際 %v", got)
	}
	if got := difficultyFeeMultiplier("high"); got != 3.0 {
		t.Fatalf("high 應為 3，實際 %v", got)
	}
	s := newAIHost(t)
	if err := s.node.walletSvc.Deposit(testAlice, wallet.AssetTACm, wallet.NewAmountF(1000, wallet.AssetTACm), "test:topup"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	// rules 模型 ×1 基礎；高難度再 ×3 → 基礎費 0.0001 × 3 = 0.0003。
	hi, err := s.node.meterAgentTurn(testAlice, false, "rules", "high")
	if err != nil {
		t.Fatalf("meter high: %v", err)
	}
	if d := hi.Amount - 0.0003; d > 1e-12 || d < -1e-12 {
		t.Fatalf("高難度 rules 應為 0.0003，實際 %v", hi.Amount)
	}
	med, err := s.node.meterAgentTurn(testAlice, false, "rules", "medium")
	if err != nil {
		t.Fatalf("meter medium: %v", err)
	}
	if d := med.Amount - 0.00015; d > 1e-12 || d < -1e-12 {
		t.Fatalf("中難度 rules 應為 0.00015，實際 %v", med.Amount)
	}
}
