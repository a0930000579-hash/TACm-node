package aiverify

// verify.go — 驗證者註冊/質押提領、跨鏈任務提交、attest 投票、quorum 定案與結算（獎勵分攤＋slashing）。
// 本檔只維護狀態；實際 TACm 移轉由 node rpc 依 SettleResult 執行。

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
)

// RegisterVerifier 註冊／追加質押為驗證者。stake 為本次質押數（節點層已先把 TACm 從 verifier 轉到 StakeAccount）。
// 已存在者在既有 stake 上累加；狀態回到 active。
func (s *Store) RegisterVerifier(addr string, stake float64) (*Verifier, error) {
	addr = norm(addr)
	if addr == "" {
		return nil, fmt.Errorf("aiverify: 缺少驗證者地址")
	}
	if stake < 0 {
		return nil, fmt.Errorf("aiverify: 質押不可為負")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var existing Verifier
	row := s.db.QueryRow(`SELECT address, stake, status, reputation, slashed_count, enabled, created_at, updated_at
		FROM aiv_verifiers WHERE address=?`, addr)
	err := row.Scan(&existing.Address, &existing.Stake, &existing.Status, &existing.Reputation,
		&existing.SlashedCount, &existing.Enabled, &existing.CreatedAt, &existing.UpdatedAt)
	switch {
	case err == sql.ErrNoRows:
		if stake < s.params.MinStake {
			return nil, fmt.Errorf("aiverify: 最低質押 %.4f TACm（本次 %.4f）", s.params.MinStake, stake)
		}
		_, err = s.db.Exec(`INSERT INTO aiv_verifiers(address,stake,status,reputation,slashed_count,enabled,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, addr, stake, VerifierActive, 0, 0, 1, now, now)
		if err != nil {
			return nil, fmt.Errorf("aiverify: 寫入驗證者: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("aiverify: 查詢驗證者: %w", err)
	default:
		newStake := round4(existing.Stake + stake)
		if newStake < s.params.MinStake {
			return nil, fmt.Errorf("aiverify: 質押後 %.4f 低於最低 %.4f", newStake, s.params.MinStake)
		}
		_, err = s.db.Exec(`UPDATE aiv_verifiers SET stake=?, status=?, enabled=1, updated_at=? WHERE address=?`,
			newStake, VerifierActive, now, addr)
		if err != nil {
			return nil, fmt.Errorf("aiverify: 更新驗證者質押: %w", err)
		}
	}
	return s.getVerifierLocked(addr)
}

// WithdrawVerifier 提領部分質押。stake 不可低於 MinStake（全額退場則 status 轉 inactive）。
// 回傳可提領金額（節點層據此從 StakeAccount 轉回 verifier）。
func (s *Store) WithdrawVerifier(addr string, amount float64) (float64, *Verifier, error) {
	addr = norm(addr)
	if addr == "" {
		return 0, nil, fmt.Errorf("aiverify: 缺少驗證者地址")
	}
	if amount < 0 {
		return 0, nil, fmt.Errorf("aiverify: 提領不可為負")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.getVerifierLocked(addr)
	if err != nil {
		return 0, nil, err
	}
	if !v.Enabled || v.Status == VerifierInactive {
		return 0, nil, fmt.Errorf("aiverify: 驗證者未啟用或已退場")
	}
	if v.Status == VerifierLocked {
		return 0, nil, fmt.Errorf("aiverify: 質押鎖定中（近期被 slash），暫無法提領")
	}
	leaveAll := amount == 0
	if !leaveAll && amount > v.Stake {
		return 0, nil, fmt.Errorf("aiverify: 提領 %.4f 超過質押 %.4f", amount, v.Stake)
	}
	now := s.now()
	if leaveAll {
		amount = v.Stake
		_, err = s.db.Exec(`UPDATE aiv_verifiers SET stake=0, status=?, enabled=0, updated_at=? WHERE address=?`,
			VerifierInactive, now, addr)
	} else {
		remain := round4(v.Stake - amount)
		if remain < s.params.MinStake {
			return 0, nil, fmt.Errorf("aiverify: 提領後質押 %.4f 低於最低 %.4f（請全額退場）", remain, s.params.MinStake)
		}
		_, err = s.db.Exec(`UPDATE aiv_verifiers SET stake=?, updated_at=? WHERE address=?`, remain, now, addr)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("aiverify: 更新提領: %w", err)
	}
	v2, _ := s.getVerifierLocked(addr)
	return round4(amount), v2, nil
}

// GetVerifier 依地址取驗證者。
func (s *Store) GetVerifier(addr string) (*Verifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getVerifierLocked(norm(addr))
}

// getVerifierLocked 呼叫方須已持鎖。
func (s *Store) getVerifierLocked(addr string) (*Verifier, error) {
	var v Verifier
	var enabled int
	err := s.db.QueryRow(`SELECT address, stake, status, reputation, slashed_count, enabled, created_at, updated_at
		FROM aiv_verifiers WHERE address=?`, addr).
		Scan(&v.Address, &v.Stake, &v.Status, &v.Reputation, &v.SlashedCount, &enabled, &v.CreatedAt, &v.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("aiverify: 驗證者不存在: %s", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("aiverify: 查詢驗證者: %w", err)
	}
	v.Enabled = enabled == 1
	return &v, nil
}

// ListVerifiers 列出全部驗證者（依正確次數／質押排序）。
func (s *Store) ListVerifiers() ([]Verifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT address, stake, status, reputation, slashed_count, enabled, created_at, updated_at
		FROM aiv_verifiers ORDER BY reputation DESC, stake DESC, updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 驗證者列表: %w", err)
	}
	defer rows.Close()
	out := make([]Verifier, 0, 8)
	for rows.Next() {
		var v Verifier
		var enabled int
		if err := rows.Scan(&v.Address, &v.Stake, &v.Status, &v.Reputation, &v.SlashedCount, &enabled, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// SubmitTask 提交一筆跨鏈 AI 推理結果驗證任務（task_fee 為獎勵池；節點層已先從 submitter 轉到 RewardAccount）。
// 挑戰窗口採用網路預設參數。
func (s *Store) SubmitTask(sourceChain string, modelID int64, modelVersion, inputCID, inputHash,
	submitter, expectedOutHash string, taskFee float64) (*InferenceTask, error) {
	return s.SubmitTaskWithWindow(sourceChain, modelID, modelVersion, inputCID, inputHash,
		submitter, expectedOutHash, taskFee, 0)
}

// SubmitTaskWithWindow 同上，但可自訂挑戰窗口（秒）；windowSec<=0 用預設。
func (s *Store) SubmitTaskWithWindow(sourceChain string, modelID int64, modelVersion, inputCID, inputHash,
	submitter, expectedOutHash string, taskFee float64, windowSec int64) (*InferenceTask, error) {
	submitter = norm(submitter)
	if submitter == "" {
		return nil, fmt.Errorf("aiverify: 缺少 submitter 地址")
	}
	if strings.TrimSpace(expectedOutHash) == "" {
		return nil, fmt.Errorf("aiverify: 缺少 expected_output_hash")
	}
	if strings.TrimSpace(inputCID) == "" && strings.TrimSpace(inputHash) == "" {
		return nil, fmt.Errorf("aiverify: 至少需提供 input_cid 或 input_hash 其一")
	}
	if taskFee < 0 {
		return nil, fmt.Errorf("aiverify: 任務費不可為負")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	chain := strings.TrimSpace(sourceChain)
	if chain == "" {
		chain = "TACm"
	}
	window := s.params.ChallengeWindowS
	if windowSec > 0 {
		window = windowSec
	}
	res, err := s.db.Exec(`INSERT INTO aiv_tasks
		(source_chain, model_id, model_version, input_cid, input_hash, submitter, expected_output_hash,
		 status, task_fee, reward_pool, created_at, challenge_deadline, settled_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0)`,
		chain, modelID, strings.TrimSpace(modelVersion), strings.TrimSpace(inputCID), strings.TrimSpace(inputHash),
		submitter, strings.TrimSpace(expectedOutHash), StatusPending, taskFee, taskFee, now, now+window)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 寫入任務: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.getTaskLocked(id)
}

// GetTask 依 id 取任務。
func (s *Store) GetTask(id int64) (*InferenceTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getTaskLocked(id)
}

// getTaskLocked 呼叫方須已持鎖。
func (s *Store) getTaskLocked(id int64) (*InferenceTask, error) {
	var t InferenceTask
	err := s.db.QueryRow(`SELECT id, source_chain, model_id, model_version, input_cid, input_hash, submitter,
		expected_output_hash, status, task_fee, reward_pool, created_at, challenge_deadline, settled_at
		FROM aiv_tasks WHERE id=?`, id).
		Scan(&t.Id, &t.SourceChain, &t.ModelId, &t.ModelVersion, &t.InputCID, &t.InputHash, &t.Submitter,
			&t.ExpectedOutputHash, &t.Status, &t.TaskFee, &t.RewardPool, &t.CreatedAt, &t.ChallengeDeadline, &t.SettledAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("aiverify: 任務不存在 (id=%d)", id)
	}
	if err != nil {
		return nil, fmt.Errorf("aiverify: 查詢任務: %w", err)
	}
	return &t, nil
}

// ListTasks 列出任務（status 空＝全部；預設最新）。
func (s *Store) ListTasks(status string) ([]InferenceTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `SELECT id, source_chain, model_id, model_version, input_cid, input_hash, submitter,
		expected_output_hash, status, task_fee, reward_pool, created_at, challenge_deadline, settled_at
		FROM aiv_tasks`
	args := []any{}
	if st := strings.TrimSpace(status); st != "" {
		q += ` WHERE status=?`
		args = append(args, st)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 200`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 任務列表: %w", err)
	}
	defer rows.Close()
	out := make([]InferenceTask, 0, 8)
	for rows.Next() {
		var t InferenceTask
		if err := rows.Scan(&t.Id, &t.SourceChain, &t.ModelId, &t.ModelVersion, &t.InputCID, &t.InputHash, &t.Submitter,
			&t.ExpectedOutputHash, &t.Status, &t.TaskFee, &t.RewardPool, &t.CreatedAt, &t.ChallengeDeadline, &t.SettledAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAttestations 列出某任務的所有投票。
func (s *Store) ListAttestations(taskID int64) ([]Attestation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, task_id, verifier, decision, claim_hash, signature, created_at
		FROM aiv_attestations WHERE task_id=? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 投票列表: %w", err)
	}
	defer rows.Close()
	out := make([]Attestation, 0, 4)
	for rows.Next() {
		var a Attestation
		if err := rows.Scan(&a.Id, &a.TaskId, &a.Verifier, &a.Decision, &a.ClaimHash, &a.Signature, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Attest 驗證者對任務投票。達 quorum 時直接結算並回傳 SettleResult（節點層據此出獎/執行 slash）；
// 未達門檻回 (nil, nil)。重複投票、非驗證者、任務已結案等皆回錯誤。
func (s *Store) Attest(taskID int64, verifier, decision, claimHash, signature string) (*SettleResult, error) {
	verifier = norm(verifier)
	decision = strings.ToLower(strings.TrimSpace(decision))
	if verifier == "" {
		return nil, fmt.Errorf("aiverify: 缺少 verifier 地址")
	}
	if decision != DecisionApprove && decision != DecisionReject {
		return nil, fmt.Errorf("aiverify: 非法決議 %q（應 approve/reject）", decision)
	}
	if decision == DecisionReject && strings.TrimSpace(claimHash) == "" {
		return nil, fmt.Errorf("aiverify: reject 須提出主張 output hash（claim_hash）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	v, err := s.getVerifierLocked(verifier)
	if err != nil {
		return nil, err
	}
	if !v.Enabled || v.Status != VerifierActive {
		return nil, fmt.Errorf("aiverify: 驗證者未啟用或質押狀態異常（status=%s）", v.Status)
	}
	if v.Stake < s.params.MinStake {
		return nil, fmt.Errorf("aiverify: 驗證者質押 %.4f 低於最低 %.4f", v.Stake, s.params.MinStake)
	}
	task, err := s.getTaskLocked(taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != StatusPending {
		return nil, fmt.Errorf("aiverify: 任務已結案（status=%s, id=%d）", task.Status, taskID)
	}
	// 重複投票預檢（友善錯誤；DB UNIQUE 為最終防線）。
	var dup int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM aiv_attestations WHERE task_id=? AND verifier=?`, taskID, verifier).Scan(&dup); err != nil {
		return nil, fmt.Errorf("aiverify: 重複投票檢查: %w", err)
	}
	if dup > 0 {
		return nil, fmt.Errorf("aiverify: 驗證者 %s 已對任務 %d 投過票", verifier, taskID)
	}

	now := s.now()
	if _, err := s.db.Exec(`INSERT INTO aiv_attestations(task_id, verifier, decision, claim_hash, signature, created_at)
		VALUES(?,?,?,?,?,?)`, taskID, verifier, decision, strings.TrimSpace(claimHash), strings.TrimSpace(signature), now); err != nil {
		return nil, fmt.Errorf("aiverify: 寫入投票: %w", err)
	}

	attestations, err := s.listAttestationsLocked(taskID)
	if err != nil {
		return nil, err
	}
	total := len(attestations)
	if int64(total) < s.params.MinVerifiers {
		return nil, nil // 未達最小驗證者數，續 pending。
	}

	approvers, rejecters := splitVotes(attestations)
	approveBps := int64(approvers.Len()) * 10000 / int64(total)
	rejectBps := int64(rejecters.Len()) * 10000 / int64(total)

	switch {
	case approveBps >= s.params.QuorumBps:
		return s.settleLocked(task, StatusVerified, DecisionApprove, approvers, rejecters)
	case rejectBps >= s.params.QuorumBps:
		return s.settleLocked(task, StatusRejected, DecisionReject, rejecters, approvers)
	default:
		return nil, nil // 兩邊皆未過半，續 pending。
	}
}

// settleLocked 定案：winnerSide 為多數側（correct），losers 為少數側（slashed）。呼叫方須持鎖。
func (s *Store) settleLocked(task *InferenceTask, status, winnerSide string, winners, losers voteList) (*SettleResult, error) {
	now := s.now()
	res := &SettleResult{
		TaskId: task.Id, Status: status, TotalVotes: winners.Len() + losers.Len(),
		Quorum: s.params.MinVerifiers, RewardPool: task.RewardPool, SettledAt: now,
	}
	// 共識 hash：verified 用提交者主張；rejected 取多數側（rejecters）主張中眾數。
	if status == StatusVerified {
		res.ConsensusHash = task.ExpectedOutputHash
	} else {
		res.ConsensusHash = modeClaimHash(winners)
	}

	// 獎勵分攤：正確驗證者平分獎勵池。
	res.Rewards = []RewardRecord{}
	if winners.Len() > 0 && task.RewardPool > 0 {
		per := round4(task.RewardPool / float64(winners.Len()))
		for _, a := range winners {
			res.Rewards = append(res.Rewards, RewardRecord{Verifier: a.Verifier, Amount: per})
			if err := s.bumpReputationLocked(a.Verifier); err != nil {
				return nil, err
			}
		}
	} else if winners.Len() > 0 {
		for _, a := range winners {
			_ = s.bumpReputationLocked(a.Verifier)
		}
	}

	// Slashing：少數側沒收 SlashFraction 質押。
	res.Slashes = []SlashRecord{}
	for _, a := range losers {
		v, err := s.getVerifierLocked(a.Verifier)
		if err != nil {
			return nil, err
		}
		slash := round4(v.Stake * s.params.SlashFraction)
		newStake := round4(v.Stake - slash)
		if _, err := s.db.Exec(`UPDATE aiv_verifiers SET stake=?, slashed_count=slashed_count+1, status=?, updated_at=?
			WHERE address=?`, newStake, VerifierLocked, now, a.Verifier); err != nil {
			return nil, fmt.Errorf("aiverify: slash 更新: %w", err)
		}
		res.Slashes = append(res.Slashes, SlashRecord{Verifier: a.Verifier, Stake: v.Stake, Slash: slash})
	}

	// 更新任務與結算表。
	if _, err := s.db.Exec(`UPDATE aiv_tasks SET status=?, settled_at=? WHERE id=?`, status, now, task.Id); err != nil {
		return nil, fmt.Errorf("aiverify: 更新任務狀態: %w", err)
	}
	var correctIDs, slashedIDs []string
	for _, w := range winners {
		correctIDs = append(correctIDs, w.Verifier)
	}
	for _, l := range losers {
		slashedIDs = append(slashedIDs, l.Verifier)
	}
	per := 0.0
	if len(res.Rewards) > 0 {
		per = res.Rewards[0].Amount
	}
	slashPer := 0.0
	if len(res.Slashes) > 0 {
		slashPer = res.Slashes[0].Slash
	}
	if _, err := s.db.Exec(`INSERT INTO aiv_settlements
		(task_id, status, consensus_hash, total_votes, quorum, reward_pool, reward_per_verifier,
		 correct_verifiers, slashed_verifiers, slash_per_verifier, settled_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		task.Id, status, res.ConsensusHash, res.TotalVotes, res.Quorum, res.RewardPool, per,
		strings.Join(correctIDs, ","), strings.Join(slashedIDs, ","), slashPer, now); err != nil {
		return nil, fmt.Errorf("aiverify: 寫入結算: %w", err)
	}
	return res, nil
}

// bumpReputationLocked 正確驗證者 reputation+1。
func (s *Store) bumpReputationLocked(addr string) error {
	if _, err := s.db.Exec(`UPDATE aiv_verifiers SET reputation=reputation+1, updated_at=? WHERE address=?`,
		s.now(), addr); err != nil {
		return fmt.Errorf("aiverify: 更新聲譽: %w", err)
	}
	return nil
}

// ExpirePending 把已過挑戰窗口仍未達 quorum 的 pending 任務標記為 expired，
// 並回傳需退費的 SettleResult（submitter_refund = task_fee）。節點層據此退款。
func (s *Store) ExpirePending() ([]*SettleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	rows, err := s.db.Query(`SELECT id FROM aiv_tasks WHERE status=? AND challenge_deadline<=?`, StatusPending, now)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 查詢過期任務: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*SettleResult, 0, len(ids))
	for _, id := range ids {
		task, err := s.getTaskLocked(id)
		if err != nil {
			return out, err
		}
		if _, err := s.db.Exec(`UPDATE aiv_tasks SET status=?, settled_at=? WHERE id=?`, StatusExpired, now, id); err != nil {
			return out, fmt.Errorf("aiverify: 標記過期: %w", err)
		}
		out = append(out, &SettleResult{
			TaskId: id, Status: StatusExpired, RewardPool: task.RewardPool,
			SubmitterRefund: task.TaskFee, SettledAt: now,
		})
	}
	return out, nil
}

// --- 投票分組輔助 ---

type voteList []Attestation

func (l voteList) Len() int { return len(l) }

// splitVotes 依 decision 分組。
func splitVotes(atts []Attestation) (approvers, rejecters voteList) {
	for _, a := range atts {
		switch a.Decision {
		case DecisionApprove:
			approvers = append(approvers, a)
		case DecisionReject:
			rejecters = append(rejecters, a)
		}
	}
	return approvers, rejecters
}

// modeClaimHash 取投票群中出現次數最多的 claim_hash（眾數）；全空則回空字串。
func modeClaimHash(l voteList) string {
	counts := map[string]int{}
	for _, a := range l {
		h := strings.TrimSpace(a.ClaimHash)
		if h == "" {
			continue
		}
		counts[h]++
	}
	best, bestN := "", 0
	// 排序以使結果確定性（同票數取字典序較小者）。
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if counts[k] > bestN {
			bestN = counts[k]
			best = k
		}
	}
	return best
}

// listAttestationsLocked 呼叫方須持鎖。
func (s *Store) listAttestationsLocked(taskID int64) ([]Attestation, error) {
	rows, err := s.db.Query(`SELECT id, task_id, verifier, decision, claim_hash, signature, created_at
		FROM aiv_attestations WHERE task_id=? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 投票列表: %w", err)
	}
	defer rows.Close()
	out := make([]Attestation, 0, 4)
	for rows.Next() {
		var a Attestation
		if err := rows.Scan(&a.Id, &a.TaskId, &a.Verifier, &a.Decision, &a.ClaimHash, &a.Signature, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// round4 四捨五入至 4 位小數（與 aimarket.billing 一致）。
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
