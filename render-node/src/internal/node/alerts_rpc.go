package node

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/wallet"
)

// utilWarning 依供應利用率給兩級警報（v108 後閾值可配置，此處保留預設相容）。
//   - "critical"：>=over（接近/超過市值上限，可能觸發強制銷毀）
//   - "warn"：>=near（接近上限）
//   - "ok"：其餘
func utilWarning(util float64) string {
	return utilWarningCfg(util, 0.95, 0.80)
}

// utilWarningCfg v108：依可配置閾值給警報等級。
func utilWarningCfg(util, over, near float64) string {
	if util >= over {
		return "critical"
	}
	if util >= near {
		return "warn"
	}
	return "ok"
}

// handleAlerts GET /api/alerts — 最近鏈上警報事件（30 天內）：
//   - 市值縮水自動銷毀（memo supply:cap-burn）
//   - 供給層補鑄（supply:floor-topup，供應跌破下限）
//   - 跨鏈失敗（bridge 非 completed/confirmed 記錄）
//   - 利用率突破（由 dashboard overview 即時計算，此處回傳閾值）
//
// 回傳 { ok, alerts: [{ts, type, level, message, detail}] }。
func (s *RPCServer) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	out := []map[string]any{}
	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()

	// 1. wallet ledger 事件（cap-burn / floor-topup / 大額異常）。
	ledger, err := s.node.walletSvc.Ledger(5000)
	if err == nil {
		for _, e := range ledger {
			if e.Ts < cutoff {
				continue
			}
			m := e.Memo
			delta, _ := strconv.ParseInt(e.Delta, 10, 64)
			switch {
			case strings.Contains(m, "supply:cap-burn"):
				out = append(out, map[string]any{
					"ts": e.Ts, "level": "critical", "type": "cap_burn",
					"message": "市值縮水：自動銷毀 TiUSD 至新上限",
					"detail":  string(e.Asset) + " delta=" + wallet.FmtThouI64(delta, 6, 6),
				})
			case strings.Contains(m, "supply:floor-topup"):
				out = append(out, map[string]any{
					"ts": e.Ts, "level": "warn", "type": "floor_topup",
					"message": "供應跌破下限：供給層自動補鑄",
					"detail":  string(e.Asset) + " delta=" + wallet.FmtThouI64(delta, 6, 6),
				})
			}
		}
	}

	// 2. 跨鏈失敗（bridge 非完成狀態）。
	if s.node.bridge != nil {
		txs, berr := s.node.bridge.ListBridgeTxs(200, "")
		if berr != nil {
			log.Printf("[alerts] bridge 查詢失敗: %v", berr)
		}
		if berr == nil {
			for _, tx := range txs {
				if tx.CreatedAt < cutoff {
					continue
				}
				st := strings.ToLower(tx.Status)
				if st != "completed" && st != "confirmed" && st != "pending" {
					out = append(out, map[string]any{
						"ts": tx.CreatedAt, "level": "warn", "type": "bridge_failed",
						"message": "跨鏈交易異常：",
						"detail":  tx.BridgeTxID + " " + tx.SourceChain + "→" + tx.TargetChain + " status=" + tx.Status,
					})
				}
			}
		}
	}

	// 3. 當前利用率閾值（v108：閾值可配置；tiusd dashboard 同源）。
	cfg := s.loadAlertConfig()
	if s.node.walletSvc != nil {
		if sup, serr := s.node.walletSvc.TiUSDSummary(); serr == nil {
			_, capAmt, capEnabled, cerr := s.node.walletSvc.TiUSDCap()
			if cerr == nil && capEnabled && capAmt > 0 {
				u := float64(sup.Supply) / float64(capAmt)
				lvl := utilWarningCfg(u, cfg.Over, cfg.Near)
				if lvl == "critical" {
					out = append(out, map[string]any{
						"ts": time.Now().Unix(), "level": "critical", "type": "util_critical",
						"message": fmt.Sprintf("TiUSD 供應利用率 ≥%.0f%%（接近市值上限）", cfg.Over*100),
						"detail":  wallet.FmtThouI64(sup.Supply, 6, 6) + " / " + wallet.FmtThouI64(capAmt, 6, 6),
					})
				} else if lvl == "warn" {
					out = append(out, map[string]any{
						"ts": time.Now().Unix(), "level": "warn", "type": "util_warn",
						"message": fmt.Sprintf("TiUSD 供應利用率 ≥%.0f%%", cfg.Near*100),
						"detail":  wallet.FmtThouI64(sup.Supply, 6, 6) + " / " + wallet.FmtThouI64(capAmt, 6, 6),
					})
				}
			}
		}
	}

	// 依時間新→舊排序（插入序大致即時序，簡單排序）。
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j]["ts"].(int64) > out[i]["ts"].(int64) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	s.maybeNotifyWebhook(out)
	// v116：Webhook 持久化佇列狀態（pending 待投遞數——節點重啟後續投的可觀測性）。
	pending, failed, qerr := s.node.walletSvc.Store().WebhookQueueStats()
	if qerr != nil {
		log.Printf("[alerts] webhook 佇列統計失敗: %v", qerr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alerts": out, "webhook_pending": pending, "webhook_failed": failed})
}

// ---------------------------------------------------------------------------
// v108：警報配置（閾值＋Webhook）——存 wallet settings 表。
// ---------------------------------------------------------------------------

type alertConfig struct {
	Near          float64 `json:"near"`
	Over          float64 `json:"over"`
	WebhookURL    string  `json:"webhook_url"`
	WebhookSecret string  `json:"webhook_secret"`
}

func (s *RPCServer) loadAlertConfig() alertConfig {
	cfg := alertConfig{Near: 0.95, Over: 0.99}
	if s.node.walletSvc == nil {
		return cfg
	}
	if v, err := s.node.walletSvc.GetSetting("alert.near"); err == nil && v != "" {
		if f, ferr := strconv.ParseFloat(v, 64); ferr == nil && f > 0 && f < 1 {
			cfg.Near = f
		}
	}
	if v, err := s.node.walletSvc.GetSetting("alert.over"); err == nil && v != "" {
		if f, ferr := strconv.ParseFloat(v, 64); ferr == nil && f > 0 && f < 1 {
			cfg.Over = f
		}
	}
	if v, err := s.node.walletSvc.GetSetting("alert.webhook"); err == nil {
		cfg.WebhookURL = strings.TrimSpace(v)
	}
	if v, err := s.node.walletSvc.GetSetting("alert.secret"); err == nil {
		cfg.WebhookSecret = strings.TrimSpace(v)
	}
	return cfg
}

func (s *RPCServer) saveAlertConfig(cfg alertConfig) error {
	if cfg.Near <= 0 || cfg.Near >= 1 || cfg.Over <= 0 || cfg.Over >= 1 || cfg.Near >= cfg.Over {
		return fmt.Errorf("invalid thresholds: 0 < near < over < 1")
	}
	if err := s.node.walletSvc.SetSetting("alert.near", strconv.FormatFloat(cfg.Near, 'f', 4, 64)); err != nil {
		return err
	}
	if err := s.node.walletSvc.SetSetting("alert.over", strconv.FormatFloat(cfg.Over, 'f', 4, 64)); err != nil {
		return err
	}
	if err := s.node.walletSvc.SetSetting("alert.webhook", strings.TrimSpace(cfg.WebhookURL)); err != nil {
		return err
	}
	return s.node.walletSvc.SetSetting("alert.secret", strings.TrimSpace(cfg.WebhookSecret))
}

// handleAlertConfig GET/PUT /api/alerts/config — 讀取/更新警報配置。
func (s *RPCServer) handleAlertConfig(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "wallet_disabled"})
		return
	}
	if r.Method == http.MethodPut {
		var cfg alertConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := s.saveAlertConfig(cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": s.loadAlertConfig()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": s.loadAlertConfig()})
}

// maybeNotifyWebhook 新警報出現時向自訂 webhook POST JSON（goroutine）。
// v109：支援 HMAC-SHA256 簽名（X-TAC-Signature header）＋失敗重試（3 次指數退避）。
func (s *RPCServer) maybeNotifyWebhook(alerts []map[string]any) {
	if len(alerts) == 0 {
		return
	}
	cfg := s.loadAlertConfig()
	if cfg.WebhookURL == "" {
		return
	}
	sig := ""
	for _, a := range alerts[:min(3, len(alerts))] {
		sig += fmt.Sprintf("%s:%d;", a["type"], a["ts"].(int64))
	}
	s.notifyMu.Lock()
	if sig != "" && sig == s.lastAlertSig {
		s.notifyMu.Unlock()
		return
	}
	if sig != "" {
		s.lastAlertSig = sig
	}
	s.notifyMu.Unlock()

	payload, _ := json.Marshal(map[string]any{
		"event": "tiusd_alert", "time": time.Now().Unix(), "alerts": alerts[:min(5, len(alerts))],
	})
	go func(body []byte, url, secret string) {
		// HMAC-SHA256 簽名：sha256=<hex>（payload 原樣簽）。
		sign := ""
		if secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			sign = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		client := &http.Client{Timeout: 5 * time.Second}
		// 失敗重試：最多 3 次，指數退避 1s/2s/4s。
		for attempt := 0; attempt < 3; attempt++ {
			req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				log.Printf("[alerts] webhook 請求建構失敗: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "TACm-Node/1.0")
			if sign != "" {
				req.Header.Set("X-TAC-Signature", sign)
			}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return
				}
				log.Printf("[alerts] webhook HTTP %d（第 %d 次）", resp.StatusCode, attempt+1)
			} else {
				log.Printf("[alerts] webhook 通知失敗（第 %d 次）: %v", attempt+1, err)
			}
			if attempt < 2 {
				time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
			}
		}
	}(payload, cfg.WebhookURL, cfg.WebhookSecret)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
