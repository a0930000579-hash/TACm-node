package node

// aimarket_rpc.go — 鏈上 AI 模型市場 RPC（對標 Bittensor / Ritual，簡化版）：
//   POST /api/aimarket/register       — 註冊模型（同名同 owner 自動版本遞增，保留舊版）
//   GET  /api/aimarket/list?q=&sort=  — 列表／關鍵字搜尋（popular|rating|new）
//   GET  /api/aimarket/model?id=      — 單一模型詳情
//   POST /api/aimarket/subscribe      — 訂閱 per_period 模型（付費：owner 得 ownerPart，平台抽 protocolFee）
//   POST /api/aimarket/invoke         — 呼叫模型（per_call 當次付費；per_period 憑有效訂閱免費）
//   POST /api/aimarket/rate            — 評分（曾呼叫/訂閱者）
//   GET  /api/aimarket/subscriptions?subscriber= — 我的訂閱
// 實際 TACm/TiUSD 移轉在這裡做；aimarket 包只維護純狀態。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/aimarket"
	"tacm/internal/wallet"
)

// aimMarketRelayTimeout 有 URL 的模型呼叫轉發超時（失敗即回落 stub，不影響主流程）。
const aimMarketRelayTimeout = 2 * time.Second

// aimAsset 解析請求資產（空/未知預設 TACm）。
func aimAsset(a string) wallet.Asset {
	na := normalizeAsset(a)
	if na == "" {
		return wallet.AssetTACm
	}
	return wallet.Asset(na)
}

// aimCharge 計費拆分並執行兩筆轉帳：caller→owner 得 ownerPart、caller→FeeAccount 得 protocolFee。
// 回傳 (ownerPart, protocolFee, err)。任一筆失敗時盡可能退回已轉出的部分。
func (s *RPCServer) aimCharge(caller, owner string, price float64, asset wallet.Asset, memo string) (float64, float64, error) {
	ownerPart, fee := aimarket.Split(price)
	// 1) ownerPart：caller → owner。
	if ownerPart > 0 {
		if _, err := s.node.walletSvc.Transfer(caller, owner, asset,
			wallet.NewAmountF(ownerPart, asset), memo+":owner"); err != nil {
			return 0, 0, fmt.Errorf("ownerPart 支付失敗: %w", err)
		}
	}
	// 2) protocolFee：caller → aimarket_fee。
	if fee > 0 {
		if _, err := s.node.walletSvc.Transfer(caller, aimarket.FeeAccount, asset,
			wallet.NewAmountF(fee, asset), memo+":fee"); err != nil {
			// 退回已給 owner 的部分。
			_ = s.node.walletSvc.InternalTransfer(owner, caller, asset,
				wallet.NewAmountF(ownerPart, asset), memo+":refund-owner")
			return 0, 0, fmt.Errorf("protocolFee 支付失敗: %w", err)
		}
	}
	return ownerPart, fee, nil
}

// aimRefund 計費失敗時退回（owner 與 fee 帳戶各回 caller）。
func (s *RPCServer) aimRefund(caller, owner string, ownerPart, fee float64, asset wallet.Asset, memo string) {
	if ownerPart > 0 {
		_ = s.node.walletSvc.InternalTransfer(owner, caller, asset,
			wallet.NewAmountF(ownerPart, asset), memo+":refund-owner")
	}
	if fee > 0 {
		_ = s.node.walletSvc.InternalTransfer(aimarket.FeeAccount, caller, asset,
			wallet.NewAmountF(fee, asset), memo+":refund-fee")
	}
}

// handleAIMarketRegister POST /api/aimarket/register
func (s *RPCServer) handleAIMarketRegister(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	var req struct {
		Address   string  `json:"address"`
		Name      string  `json:"name"`
		Metadata  string  `json:"metadata"`
		CID       string  `json:"cid"`
		URL       string  `json:"url"`
		Price     float64 `json:"price"`
		PriceType string  `json:"price_type"`
		PeriodSec int64   `json:"period_sec"`
		Version   string  `json:"version"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	owner, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "owner 地址無效")
		return
	}
	if req.PriceType == "" {
		req.PriceType = aimarket.PriceTypeCall
	}
	m, err := s.node.aimSt.RegisterModel(owner, req.Name, req.Metadata, req.CID, req.URL,
		req.Version, req.Price, req.PriceType, req.PeriodSec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": m})
}

// handleAIMarketList GET /api/aimarket/list?q=&sort=
func (s *RPCServer) handleAIMarketList(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	q := r.URL.Query().Get("q")
	sort := r.URL.Query().Get("sort")
	models, err := s.node.aimSt.ListModels(q, sort)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": models})
}

// handleAIMarketModel GET /api/aimarket/model?id=
func (s *RPCServer) handleAIMarketModel(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	m, err := s.node.aimSt.GetModel(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": m})
}

// handleAIMarketSubscribe POST /api/aimarket/subscribe
func (s *RPCServer) handleAIMarketSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	var req struct {
		Address string `json:"address"`
		ModelID int64  `json:"model_id"`
		Asset   string `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	caller, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "caller 地址無效")
		return
	}
	m, err := s.node.aimSt.GetModel(req.ModelID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if m.PriceType != aimarket.PriceTypePeriod {
		writeErr(w, http.StatusBadRequest, "該模型為 per_call，無需訂閱")
		return
	}
	asset := aimAsset(req.Asset)
	memo := fmt.Sprintf("aimarket:subscribe:%d", m.Id)
	ownerPart, fee, err := s.aimCharge(caller, m.Owner, m.Price, asset, memo)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "訂閱付費失敗（需 "+strconv.FormatFloat(m.Price, 'f', 4, 64)+" "+string(asset)+"）："+err.Error())
		return
	}
	sub, err := s.node.aimSt.Subscribe(caller, m.Id)
	if err != nil {
		s.aimRefund(caller, m.Owner, ownerPart, fee, asset, memo)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "subscription": sub,
		"price": m.Price, "owner_part": ownerPart, "protocol_fee": fee,
		"asset": string(asset), "fee_account": aimarket.FeeAccount,
	})
}

// aimRelay 有 URL 的模型盡力轉發（2s 超時）；失敗回 ""（呼叫方回落 stub）。
func aimRelay(modelURL string, input string) string {
	if strings.TrimSpace(modelURL) == "" {
		return ""
	}
	body, _ := json.Marshal(map[string]any{"input": input})
	cli := &http.Client{Timeout: aimMarketRelayTimeout}
	resp, err := cli.Post(modelURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var out struct {
		Output string `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Output == "" {
		return ""
	}
	return out.Output
}

// handleAIMarketInvoke POST /api/aimarket/invoke
func (s *RPCServer) handleAIMarketInvoke(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	var req struct {
		Address string `json:"address"`
		ModelID int64  `json:"model_id"`
		Input   string `json:"input"`
		Asset   string `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	caller, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "caller 地址無效")
		return
	}
	m, err := s.node.aimSt.GetModel(req.ModelID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	asset := aimAsset(req.Asset)
	var (
		charged, ownerPart, fee float64
	)
	memo := fmt.Sprintf("aimarket:invoke:%d", m.Id)

	if m.PriceType == aimarket.PriceTypePeriod {
		// 憑有效訂閱；否則拒絕。
		ok, err := s.node.aimSt.HasActiveSubscription(caller, m.Id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusForbidden, "無有效訂閱，請先訂閱此模型")
			return
		}
		charged = 0
	} else {
		// per_call：當次付費。
		ownerPart, fee, err = s.aimCharge(caller, m.Owner, m.Price, asset, memo)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "呼叫付費失敗（需 "+strconv.FormatFloat(m.Price, 'f', 4, 64)+" "+string(asset)+"）："+err.Error())
			return
		}
		charged = m.Price
	}

	// 盡力轉發；失敗即 stub。
	output := aimRelay(m.URL, req.Input)
	source := "stub"
	if output != "" {
		source = "relay"
	} else {
		output = fmt.Sprintf("[stub] model=%s v%s echo: %s", m.Name, m.Version, req.Input)
	}

	inv, err := s.node.aimSt.RecordInvocation(caller, m.Id, charged, "ok")
	if err != nil {
		if m.PriceType == aimarket.PriceTypeCall {
			s.aimRefund(caller, m.Owner, ownerPart, fee, asset, memo)
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"result": aimarket.InvokeResult{
			InvocationId: inv.Id,
			ModelId:      m.Id,
			Version:      m.Version,
			Caller:       caller,
			Output:       output,
			Source:       source,
			Charged:      charged,
			OwnerPart:    ownerPart,
			ProtocolFee:  fee,
			FeeAccount:   aimarket.FeeAccount,
			CreatedAt:    inv.CreatedAt,
		},
	})
}

// handleAIMarketRate POST /api/aimarket/rate
func (s *RPCServer) handleAIMarketRate(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	var req struct {
		Address string `json:"address"`
		ModelID int64  `json:"model_id"`
		Score   int64  `json:"score"`
		Comment string `json:"comment"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	rater, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "rater 地址無效")
		return
	}
	qual, err := s.node.aimSt.HasInvokedOrSubscribed(rater, req.ModelID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !qual {
		writeErr(w, http.StatusForbidden, "僅曾呼叫或訂閱過此模型者可評分")
		return
	}
	rating, err := s.node.aimSt.RateModel(req.ModelID, rater, req.Score, req.Comment)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m, _ := s.node.aimSt.GetModel(req.ModelID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rating": rating, "model": m})
}

// handleAIMarketSubscriptions GET /api/aimarket/subscriptions?subscriber=
func (s *RPCServer) handleAIMarketSubscriptions(w http.ResponseWriter, r *http.Request) {
	if s.node.aimSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Market 未啟用")
		return
	}
	addr := r.URL.Query().Get("subscriber")
	subscriber, err := s.node.normalizeEthAddr(addr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "subscriber 地址無效")
		return
	}
	subs, err := s.node.aimSt.ListSubscriptions(subscriber)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 附上模型摘要。
	type subView struct {
		aimarket.Subscription
		ModelName string  `json:"model_name"`
		Price     float64 `json:"price"`
	}
	out := make([]subView, 0, len(subs))
	for _, sub := range subs {
		v := subView{Subscription: sub}
		if m, err := s.node.aimSt.GetModel(sub.ModelId); err == nil {
			v.ModelName = m.Name
			v.Price = m.Price
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subscriptions": out})
}

var _ = strconv.Itoa // 保留（日後擴充）
