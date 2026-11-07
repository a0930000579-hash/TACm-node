// Package node — C2C 場外交易 RPC（擔保帳戶鏈上凍結／釋放）。
package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"tacm/internal/c2c"
	"tacm/internal/wallet"
)

// c2cSvc 回傳 C2C Store（未啟用時 ok=false）。
func (s *RPCServer) c2cSvc() (*c2c.Store, bool) {
	if s.node == nil || s.node.c2cSvc == nil {
		return nil, false
	}
	return s.node.c2cSvc, true
}

// handleC2CAds GET /api/c2c/ads?asset=&fiat=&side=
func (s *RPCServer) handleC2CAds(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	asset := r.URL.Query().Get("asset")
	if asset == "" {
		asset = "USDT"
	}
	fiat := r.URL.Query().Get("fiat")
	if fiat == "" {
		fiat = "TWD"
	}
	ads, err := cs.ListAds(asset, fiat, r.URL.Query().Get("side"), 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ads": ads})
}

// handleC2CAdCreate POST /api/c2c/ad-create
func (s *RPCServer) handleC2CAdCreate(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		Address        string   `json:"address"`
		Side           string   `json:"side"`
		Asset          string   `json:"asset"`
		Fiat           string   `json:"fiat"`
		Price          float64  `json:"price"`
		MinAmount      float64  `json:"min_amount"`
		MaxAmount      float64  `json:"max_amount"`
		PaymentMethods []string `json:"payment_methods"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	if strings.TrimSpace(req.Address) == "" {
		writeErr(w, http.StatusBadRequest, "缺少地址")
		return
	}
	id, err := cs.CreateAd(req.Address, req.Side, req.Asset, req.Fiat, req.Price, req.MinAmount, req.MaxAmount, req.PaymentMethods)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ad_id": id, "message": "廣告創建成功"})
}

// handleC2CMyAds GET /api/c2c/my-ads?address=
func (s *RPCServer) handleC2CMyAds(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "缺少 address")
		return
	}
	ads, err := cs.MyAds(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ads": ads})
}

// handleC2CAdStatus POST /api/c2c/ad-status
func (s *RPCServer) handleC2CAdStatus(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		AdID    int64  `json:"ad_id"`
		Address string `json:"address"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	if err := cs.UpdateAdStatus(req.AdID, strings.TrimSpace(req.Address), req.Status); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "廣告狀態已更新"})
}

// handleC2COrderCreate POST /api/c2c/order-create
// sell 廣告：賣家幣鏈上凍結入 escrow；buy 廣告：無凍結（放行時賣方支付）。
func (s *RPCServer) handleC2COrderCreate(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		AdID      int64   `json:"ad_id"`
		Buyer     string  `json:"buyer"`
		Amount    float64 `json:"amount"`
		PayMethod string  `json:"payment_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	req.Buyer = strings.TrimSpace(req.Buyer)
	if req.Buyer == "" || req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少買家地址或數量無效")
		return
	}
	ad, err := cs.GetAd(req.AdID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ad == nil {
		writeErr(w, http.StatusNotFound, "廣告不存在")
		return
	}
	// sell 廣告：先鏈上凍結賣家資產到 escrow（含鏈上費）。
	if ad.Side == "sell" {
		if _, err := s.node.walletSvc.Transfer(ad.Address, c2c.EscrowAccount, wallet.Asset(ad.Asset), amountOf(req.Amount, ad.Asset), fmt.Sprintf("c2c:escrow:%d", req.AdID)); err != nil {
			writeErr(w, http.StatusBadRequest, "賣家可用餘額不足（擔保凍結失敗）："+err.Error())
			return
		}
	}
	o, err := cs.CreateOrder(req.AdID, req.Buyer, req.PayMethod, req.Amount)
	if err != nil {
		// 凍結後建單失敗 → 退回 escrow。
		if ad.Side == "sell" {
			_ = s.node.walletSvc.InternalTransfer(c2c.EscrowAccount, ad.Address, wallet.Asset(ad.Asset), amountOf(req.Amount, ad.Asset), "c2c:unfreeze")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order_id": o.ID, "order_no": o.OrderNo, "total_fiat": o.TotalFiat, "message": "訂單創建成功"})
}

// handleC2COrderConfirm POST /api/c2c/order-confirm
func (s *RPCServer) handleC2COrderConfirm(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		OrderID int64  `json:"order_id"`
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	if err := cs.ConfirmPayment(req.OrderID, strings.TrimSpace(req.Address)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已確認付款，等待賣家放行"})
}

// handleC2COrderRelease POST /api/c2c/order-release
// sell 廣告：escrow 釋放給買家；buy 廣告：賣方（廣告主）向買家支付。
func (s *RPCServer) handleC2COrderRelease(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		OrderID int64  `json:"order_id"`
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	o, err := cs.GetOrder(req.OrderID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if o == nil {
		writeErr(w, http.StatusNotFound, "訂單不存在")
		return
	}
	if o.Seller != strings.TrimSpace(req.Address) {
		writeErr(w, http.StatusForbidden, "只有賣家可以放行")
		return
	}
	ad, _ := cs.GetAd(o.AdID)
	if ad == nil {
		writeErr(w, http.StatusNotFound, "廣告不存在")
		return
	}
	if ad.Side == "sell" {
		// escrow → 買家（免二次費）。
		if err := s.node.walletSvc.InternalTransfer(c2c.EscrowAccount, o.Buyer, wallet.Asset(o.Asset), amountOf(o.Amount, o.Asset), "c2c:release"); err != nil {
			writeErr(w, http.StatusInternalServerError, "放行失敗："+err.Error())
			return
		}
	} else {
		// buy 廣告：賣方（廣告主）向買家支付幣（含鏈上費）。
		if _, err := s.node.walletSvc.Transfer(o.Seller, o.Buyer, wallet.Asset(o.Asset), amountOf(o.Amount, o.Asset), "c2c:buy-release"); err != nil {
			writeErr(w, http.StatusInternalServerError, "賣家餘額不足，放行失敗："+err.Error())
			return
		}
	}
	if err := cs.Release(req.OrderID, o.Seller); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已放行，交易完成"})
}

// handleC2COrderCancel POST /api/c2c/order-cancel
func (s *RPCServer) handleC2COrderCancel(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok || s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		OrderID int64  `json:"order_id"`
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	o, err := cs.GetOrder(req.OrderID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if o == nil {
		writeErr(w, http.StatusNotFound, "訂單不存在")
		return
	}
	ad, _ := cs.GetAd(o.AdID)
	if err := cs.CancelOrder(req.OrderID, strings.TrimSpace(req.Address)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// sell 廣告取消 → escrow 釋放回賣家。
	if ad != nil && ad.Side == "sell" {
		if err := s.node.walletSvc.InternalTransfer(c2c.EscrowAccount, o.Seller, wallet.Asset(o.Asset), amountOf(o.Amount, o.Asset), "c2c:cancel-unfreeze"); err != nil {
			writeErr(w, http.StatusInternalServerError, "解除凍結失敗："+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "訂單已取消"})
}

// handleC2COrderDispute POST /api/c2c/order-dispute
func (s *RPCServer) handleC2COrderDispute(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	var req struct {
		OrderID int64  `json:"order_id"`
		Address string `json:"address"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "請填寫申訴原因")
		return
	}
	if err := cs.DisputeOrder(req.OrderID, strings.TrimSpace(req.Address), req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已提交申訴，客服將盡快處理"})
}

// handleC2CMyOrders GET /api/c2c/my-orders?address=
func (s *RPCServer) handleC2CMyOrders(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.c2cSvc()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "C2C 未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "缺少 address")
		return
	}
	orders, err := cs.MyOrders(addr, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "orders": orders})
}
