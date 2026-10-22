package node

// aiverify_rpc.go — 跨鏈 AI 推理驗證 RPC（對標 Bittensor ROORS / Ritual 驗證網路，簡化版）：
//   POST /api/aiverify/verifier/register  — 驗證者質押註冊（verifier → aivStake）
//   POST /api/aiverify/verifier/withdraw  — 驗證者提領質押（aivStake → verifier）
//   POST /api/aiverify/task/submit        — 跨鏈提交推理結果（submitter → aivReward 任務費）
//   POST /api/aiverify/attest             — 驗證者投票（達 quorum 自動結算：出獎／slash）
//   POST /api/aiverify/infer              — 編排：呼叫 aimarket 模型→提交驗證任務
//   GET  /api/aiverify/task?id=           — 單一任務（含投票與結算）
//   GET  /api/aiverify/tasks              — 任務列表（?status=）
//   GET  /api/aiverify/verifiers          — 驗證者排行
// 實際 TACm 移轉在這裡做；aiverify 包只維護純狀態。金流全走 InternalTransfer（無手續費）。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/aimarket"
	"tacm/internal/aiverify"
	"tacm/internal/wallet"
)

// aivAsset 解析請求資產（空/未知預設 TACm）。
func aivAsset(a string) wallet.Asset {
	na := normalizeAsset(a)
	if na == "" {
		return wallet.AssetTACm
	}
	return wallet.Asset(na)
}

// aivEnabled 回傳 aiverify 是否已就緒。
func (s *RPCServer) aivEnabled() bool {
	return s.node.aivSt != nil && s.node.walletSvc != nil
}

// aivSettleMoney 依 SettleResult 執行獎勵出金（RewardAccount→正確者）與 slash 沒收（StakeAccount→RewardAccount）。
// 最佳努力：單筆失敗不回滾已定案的狀態，僅記錄於回包。
func (s *RPCServer) aivSettleMoney(res *aiverify.SettleResult, asset wallet.Asset) []string {
	var issues []string
	for _, rw := range res.Rewards {
		if rw.Amount <= 0 {
			continue
		}
		if err := s.node.walletSvc.InternalTransfer(aiverify.RewardAccount, rw.Verifier, asset,
			wallet.NewAmountF(rw.Amount, asset), "aiverify:reward"); err != nil {
			issues = append(issues, fmt.Sprintf("reward %s: %v", rw.Verifier, err))
		}
	}
	for _, sl := range res.Slashes {
		if sl.Slash <= 0 {
			continue
		}
		if err := s.node.walletSvc.InternalTransfer(aiverify.StakeAccount, aiverify.RewardAccount, asset,
			wallet.NewAmountF(sl.Slash, asset), "aiverify:slash"); err != nil {
			issues = append(issues, fmt.Sprintf("slash %s: %v", sl.Verifier, err))
		}
	}
	return issues
}

// aivExpireLazy 讀取前先把過期任務退款（submitter ← RewardAccount）。
func (s *RPCServer) aivExpireLazy(asset wallet.Asset) {
	expired, err := s.node.aivSt.ExpirePending()
	if err != nil {
		return
	}
	for _, e := range expired {
		if e.SubmitterRefund <= 0 {
			continue
		}
		_ = s.node.walletSvc.InternalTransfer(aiverify.RewardAccount, s.aivTaskSubmitter(e.TaskId), asset,
			wallet.NewAmountF(e.SubmitterRefund, asset), "aiverify:expire-refund")
	}
}

// aivTaskSubmitter 取任務提交者（已正規化 tx0）。
func (s *RPCServer) aivTaskSubmitter(id int64) string {
	t, err := s.node.aivSt.GetTask(id)
	if err != nil {
		return ""
	}
	return t.Submitter
}

// handleAIVerifyVerifierRegister POST /api/aiverify/verifier/register
func (s *RPCServer) handleAIVerifyVerifierRegister(w http.ResponseWriter, r *http.Request) {
	if !s.aivEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Stake   float64 `json:"stake"`
		Asset   string  `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "地址無效")
		return
	}
	asset := aivAsset(req.Asset)
	// 1) verifier → StakeAccount。
	if req.Stake > 0 {
		if _, err := s.node.walletSvc.Transfer(addr, aiverify.StakeAccount, asset,
			wallet.NewAmountF(req.Stake, asset), "aiverify:stake"); err != nil {
			writeErr(w, http.StatusBadRequest, "質押轉帳失敗（餘額不足？）："+err.Error())
			return
		}
	}
	// 2) 寫狀態；失敗則退回。
	v, err := s.node.aivSt.RegisterVerifier(addr, req.Stake)
	if err != nil {
		if req.Stake > 0 {
			_ = s.node.walletSvc.InternalTransfer(aiverify.StakeAccount, addr, asset,
				wallet.NewAmountF(req.Stake, asset), "aiverify:stake-refund")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "verifier": v, "stake_account": aiverify.StakeAccount})
}

// handleAIVerifyVerifierWithdraw POST /api/aiverify/verifier/withdraw
func (s *RPCServer) handleAIVerifyVerifierWithdraw(w http.ResponseWriter, r *http.Request) {
	if !s.aivEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	var req struct {
		Address string  `json:"address"`
		Amount  float64 `json:"amount"` // 0 = 全額退場
		Asset   string  `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "地址無效")
		return
	}
	asset := aivAsset(req.Asset)
	amount, v, err := s.node.aivSt.WithdrawVerifier(addr, req.Amount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if amount > 0 {
		if err := s.node.walletSvc.InternalTransfer(aiverify.StakeAccount, addr, asset,
			wallet.NewAmountF(amount, asset), "aiverify:withdraw"); err != nil {
			writeErr(w, http.StatusInternalServerError, "出金失敗: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "withdrawn": amount, "verifier": v})
}

// handleAIVerifyTaskSubmit POST /api/aiverify/task/submit
func (s *RPCServer) handleAIVerifyTaskSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.aivEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	var req struct {
		Address            string  `json:"address"`
		SourceChain        string  `json:"source_chain"`
		ModelID            int64   `json:"model_id"`
		ModelVersion       string  `json:"model_version"`
		InputCID           string  `json:"input_cid"`
		InputHash          string  `json:"input_hash"`
		ExpectedOutputHash string  `json:"expected_output_hash"`
		TaskFee            float64 `json:"task_fee"`
		ChallengeSec       int64   `json:"challenge_sec"`
		Asset              string  `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "地址無效")
		return
	}
	asset := aivAsset(req.Asset)
	// 1) submitter → RewardAccount（任務費＝獎勵池；協議託管，無手續費，與質押同質）。
	if req.TaskFee > 0 {
		if err := s.node.walletSvc.InternalTransfer(addr, aiverify.RewardAccount, asset,
			wallet.NewAmountF(req.TaskFee, asset), "aiverify:taskfee"); err != nil {
			writeErr(w, http.StatusBadRequest, "任務費轉帳失敗（餘額不足？）："+err.Error())
			return
		}
	}
	task, err := s.node.aivSt.SubmitTaskWithWindow(req.SourceChain, req.ModelID, req.ModelVersion,
		req.InputCID, req.InputHash, addr, req.ExpectedOutputHash, req.TaskFee, req.ChallengeSec)
	if err != nil {
		if req.TaskFee > 0 {
			_ = s.node.walletSvc.InternalTransfer(aiverify.RewardAccount, addr, asset,
				wallet.NewAmountF(req.TaskFee, asset), "aiverify:taskfee-refund")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task, "reward_account": aiverify.RewardAccount})
}

// handleAIVerifyAttest POST /api/aiverify/attest
func (s *RPCServer) handleAIVerifyAttest(w http.ResponseWriter, r *http.Request) {
	if !s.aivEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	var req struct {
		Address   string `json:"address"`
		TaskID    int64  `json:"task_id"`
		Decision  string `json:"decision"`
		ClaimHash string `json:"claim_hash"`
		Signature string `json:"signature"`
		Asset     string `json:"asset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "地址無效")
		return
	}
	asset := aivAsset(req.Asset)
	res, err := s.node.aivSt.Attest(req.TaskID, addr, req.Decision, req.ClaimHash, req.Signature)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"ok": true, "settled": res != nil}
	if res != nil {
		out["settle"] = res
		out["money_issues"] = s.aivSettleMoney(res, asset)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAIVerifyTask GET /api/aiverify/task?id=
func (s *RPCServer) handleAIVerifyTask(w http.ResponseWriter, r *http.Request) {
	if s.node.aivSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	s.aivExpireLazy(aivAsset(r.URL.Query().Get("asset")))
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	task, err := s.node.aivSt.GetTask(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	atts, _ := s.node.aivSt.ListAttestations(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task, "attestations": atts})
}

// handleAIVerifyTasks GET /api/aiverify/tasks?status=
func (s *RPCServer) handleAIVerifyTasks(w http.ResponseWriter, r *http.Request) {
	if s.node.aivSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	s.aivExpireLazy(aivAsset(r.URL.Query().Get("asset")))
	tasks, err := s.node.aivSt.ListTasks(r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tasks": tasks})
}

// handleAIVerifyVerifiers GET /api/aiverify/verifiers
func (s *RPCServer) handleAIVerifyVerifiers(w http.ResponseWriter, r *http.Request) {
	if s.node.aivSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify 未啟用")
		return
	}
	vs, err := s.node.aivSt.ListVerifiers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "verifiers": vs,
		"min_stake": s.node.aivSt.GetParams().MinStake})
}

// handleAIVerifyInfer POST /api/aiverify/infer
// 編排：呼叫 aimarket 模型（per_call 計費）→ 產出 output → 提交 aiverify 驗證任務。
func (s *RPCServer) handleAIVerifyInfer(w http.ResponseWriter, r *http.Request) {
	if !s.aivEnabled() || s.node.aimSt == nil {
		writeErr(w, http.StatusServiceUnavailable, "AI Verify / AI Market 未啟用")
		return
	}
	var req struct {
		Address      string  `json:"address"`
		ModelID      int64   `json:"model_id"`
		Input        string  `json:"input"`
		TaskFee      float64 `json:"task_fee"`
		ChallengeSec int64   `json:"challenge_sec"`
		Asset        string  `json:"asset"`
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
	asset := aivAsset(req.Asset)

	// 1) 呼叫模型：per_call 當次付費（與 aimarket.invoke 同金流）。
	var ownerPart, fee float64
	if m.PriceType == aimarket.PriceTypeCall && m.Price > 0 {
		ownerPart, fee, err = s.aimCharge(caller, m.Owner, m.Price, asset, fmt.Sprintf("aiverify:infer:%d", m.Id))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "模型呼叫付費失敗："+err.Error())
			return
		}
	}
	output := fmt.Sprintf("[stub] model=%s v%s echo: %s", m.Name, m.Version, req.Input)
	inv, err := s.node.aimSt.RecordInvocation(caller, m.Id, m.Price, "ok")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 2) 建立驗證任務：把本次推理結果（input/output 指紋）提交到 aiverify。
	vr := aimarket.BuildVerifyRequest(caller, *m, req.Input, output, req.TaskFee)
	var task *aiverify.InferenceTask
	if req.TaskFee > 0 {
		if terr := s.node.walletSvc.InternalTransfer(caller, aiverify.RewardAccount, asset,
			wallet.NewAmountF(req.TaskFee, asset), "aiverify:infer-taskfee"); terr != nil {
			writeErr(w, http.StatusBadRequest, "任務費轉帳失敗："+terr.Error())
			return
		}
	}
	task, err = s.node.aivSt.SubmitTaskWithWindow(vr.SourceChain, vr.ModelId, vr.ModelVersion,
		"", vr.InputHash, caller, vr.OutputHash, req.TaskFee, req.ChallengeSec)
	if err != nil {
		// 退回任務費。
		if req.TaskFee > 0 {
			_ = s.node.walletSvc.InternalTransfer(aiverify.RewardAccount, caller, asset,
				wallet.NewAmountF(req.TaskFee, asset), "aiverify:infer-taskfee-refund")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"invocation": inv,
		"output":     output,
		"charged":    m.Price, "owner_part": ownerPart, "protocol_fee": fee,
		"verify_task": task,
		"note":        strings.TrimSpace("驗證任務已提交，待驗證者投票達 quorum 後自動結算出獎／slash"),
	})
}
