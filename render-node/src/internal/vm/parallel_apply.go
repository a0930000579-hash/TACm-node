package vm

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"runtime"
	"sync"
)

// ParallelCallItem 為一次 ApplyCallsParallel 呼叫中的單筆合約呼叫輸入。
// 位址一律為規範化 0x 小寫；Gas 為 0 時使用 manager 預設 gas 上限。
type ParallelCallItem struct {
	To0x     string
	From0x   string
	Calldata []byte
	Value    *big.Int
	Gas      uint64
}

// parallelWorkerLimit 為單 layer 內同時執行的 goroutine 上限（防大區塊佔滿 CPU）。
var parallelWorkerLimit = runtime.NumCPU()

// cloneSnap 深拷貝一份 Snapshot（每個 fork 獨立持有，避免共享可變狀態）。
func cloneSnap(snap map[string]*WorldAccount) map[string]*WorldAccount {
	out := make(map[string]*WorldAccount, len(snap))
	for a, acc := range snap {
		out[a] = acc.clone()
	}
	return out
}

// runCallOnWorld 在指定 fork WorldState 上執行合約呼叫（不加鎖、不持久化）。
// 語義與 runCallWithGas 完全一致，只差作用的 world 可為 fork。
func runCallOnWorld(w *WorldState, m *ContractManager, addr0x, caller0x string,
	calldata []byte, callValue *big.Int, gas uint64) *CallResult {
	addr := NormalizeAddress(addr0x)
	caller := NormalizeAddress(caller0x)
	if callValue == nil {
		callValue = new(big.Int)
	}

	// precompile 直通（與 runCallWithGas 相同）。
	if tb := addrToInt(addr); tb != nil && tb.Sign() > 0 && tb.Uint64() <= 9 {
		ctx := m.newCtxGas(addr, caller, caller, callValue, gas)
		out, matched, pcGas, perr := runPrecompile(ctx, tb, calldata)
		if matched {
			if perr != nil {
				return &CallResult{OK: false, Reverted: true, GasUsed: pcGas, Error: perr.Error()}
			}
			return &CallResult{OK: true, GasUsed: pcGas, ReturnData: hex.EncodeToString(out)}
		}
	}
	if !w.HasCode(addr) {
		return &CallResult{OK: false, Error: "contract not deployed"}
	}

	ctx := m.newCtxGas(addr, caller, caller, callValue, gas)
	interp := NewInterpreter(ctx, w, 0, false)
	res := interp.Execute(w.GetCode(addr), calldata)

	if res.Reverted {
		return &CallResult{
			OK: false, Reverted: true, GasUsed: res.GasUsed,
			ReturnData:   hex.EncodeToString(res.ReturnData),
			RevertReason: firstStr(res.Err, res.RevertReason),
		}
	}
	return &CallResult{
		OK: true, GasUsed: res.GasUsed,
		Logs:       res.Logs,
		ReturnData: hex.EncodeToString(res.ReturnData),
	}
}

// storageSlotChanged 判斷 fork 版 storage 是否相對於 baseline 有任何槽不同。
func storageSlotChanged(bacc, facc *WorldAccount) bool {
	if len(facc.Storage) != len(bacc.Storage) {
		return true
	}
	for k, fv := range facc.Storage {
		bv, ok := bacc.Storage[k]
		if !ok || fv.Cmp(bv) != 0 {
			return true
		}
	}
	return false
}

// changedSlots 回傳 fork 相對於 baseline「有變動的 storage slot」（fork 值）。
func changedSlots(bacc, facc *WorldAccount) map[string]*big.Int {
	out := map[string]*big.Int{}
	for k, fv := range facc.Storage {
		bv, ok := bacc.Storage[k]
		if !ok || fv.Cmp(bv) != 0 {
			out[k] = fv
		}
	}
	return out
}

// diffTouched 回傳 fork 相對於 baseline 的「變動賬戶」集合：addr → fork 版賬戶。
func diffTouched(baseline, fork map[string]*WorldAccount) map[string]*WorldAccount {
	touched := map[string]*WorldAccount{}
	for addr, facc := range fork {
		bacc, ok := baseline[addr]
		if !ok {
			touched[addr] = facc
			continue
		}
		if facc.Balance.Cmp(bacc.Balance) != 0 ||
			!bytes.Equal(facc.Code, bacc.Code) ||
			facc.Nonce != bacc.Nonce ||
			storageSlotChanged(bacc, facc) {
			touched[addr] = facc
		}
	}
	return touched
}

// ApplyCallsParallel 對一組「連續、合格的 vm:call」合約呼叫做樂觀並行執行：
//
//   - 一次持 m.mu 鎖，逐 layer 把每筆丟到各自 fork 的 WorldState 上以 goroutine 跑 EVM；
//   - join 後做事後驗證：每筆實際寫入賬戶必須落在「預測外部集合∪自身目標合約」內，
//     且跨 fork 不得對同一賬戶的同一 storage slot（或 balance/code/nonce 欄位）寫入——
//     任何違規即整段 abort、回滾到段起點，回傳 fellBack=true 交由呼叫端逐筆串行重放；
//   - 驗證通過後按原始順序把各 fork 寫入合併回 m.world，並呼叫既有 persistState 持久化
//     （持久化保持串行、確定性）。
//
// 回傳 results 為「原始交易順序」；fellBack=true 時 results 為空、m.world 已回滾到段起點。
// 任何一筆 !OK 或靜態不合格 → 一律 fellBack（不併行），由呼叫端串行重放以得到逐字一致結果。
func (m *ContractManager) ApplyCallsParallel(items []ParallelCallItem) (results []*CallResult, fellBack bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(items) == 0 {
		return nil, false, nil
	}

	// 1) 靜態分析：逐筆建立預測集合；任一不合格 → 直接 fellBack。
	preds := make([]map[string]struct{}, len(items))
	targets := make([]string, len(items))
	for i := range items {
		it := &items[i]
		to := NormalizeAddress(it.To0x)
		caller := NormalizeAddress(it.From0x)
		targets[i] = to
		code := m.world.GetCode(to)
		elig, pred := CallParallelEligible(code, caller, to, it.Calldata)
		if !elig {
			return nil, true, nil
		}
		preds[i] = pred
	}

	// 2) 分層排程。
	layers := ScheduleLayers(len(items), preds, targets)

	results = make([]*CallResult, len(items))
	sectionSnap := m.world.Snapshot() // 整段起點 abort 基線

	for _, layer := range layers {
		layerSnap := m.world.Snapshot()
		type forkOut struct {
			res  *CallResult
			snap map[string]*WorldAccount
		}
		outs := make([]*forkOut, len(items))

		sem := make(chan struct{}, parallelWorkerLimit)
		var wg sync.WaitGroup
		for _, i := range layer {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				fork := NewWorldState()
				fork.Revert(cloneSnap(layerSnap))
				it := &items[i]
				gas := it.Gas
				if gas == 0 {
					gas = m.defaultGas
				}
				res := runCallOnWorld(fork, m, it.To0x, it.From0x, it.Calldata, it.Value, gas)
				outs[i] = &forkOut{res: res, snap: fork.Snapshot()}
			}(i)
		}
		wg.Wait()
		close(sem)

		// 任一筆 !OK → 整段回滾回退串行（與串行路徑「首筆失敗即中止」語義對齊）。
		for _, i := range layer {
			if outs[i] == nil || !outs[i].res.OK {
				m.world.Revert(sectionSnap)
				return nil, true, nil
			}
		}

		// 3) 事後驗證：計算每筆實際觸及的賬戶。
		touched := make([]map[string]*WorldAccount, len(items))
		for _, i := range layer {
			touched[i] = diffTouched(layerSnap, outs[i].snap)
			// 保守：任一 fork 刪除了基線存在的賬戶（SELFDESTRUCT 等）→ 不確定，整段回退。
			for addr := range layerSnap {
				if _, ok := outs[i].snap[addr]; !ok {
					m.world.Revert(sectionSnap)
					return nil, true, nil
				}
			}
		}
		// 3a) 寫入不得超出預測集合∪自身目標。
		for _, i := range layer {
			allowed := preds[i]
			for addr := range touched[i] {
				if _, ok := allowed[addr]; !ok {
					// 寫到預測外（含跨合約存取）→ 整段 abort。
					m.world.Revert(sectionSnap)
					return nil, true, nil
				}
			}
		}
		// 3b) 跨 fork 衝突檢測：同一賬戶被 ≥2 fork 觸及時，storage slot 與
		//     balance/code/nonce 欄位不得重疊（共享目標合約的不相交 slot 除外）。
		acctByFork := map[string][]int{}           // addr → 觸及它的 fork idx
		slotWrite := map[string]map[string][]int{} // addr→slot → 寫它的 fork idx
		for _, i := range layer {
			for addr, facc := range touched[i] {
				acctByFork[addr] = append(acctByFork[addr], i)
				bacc := layerSnap[addr] // 可能 nil（新建賬戶）
				if bacc != nil {
					for slot := range changedSlots(bacc, facc) {
						if slotWrite[addr] == nil {
							slotWrite[addr] = map[string][]int{}
						}
						slotWrite[addr][slot] = append(slotWrite[addr][slot], i)
					}
				}
			}
		}
		for addr, forks := range acctByFork {
			if len(forks) < 2 {
				continue
			}
			// 同一賬戶被多 fork 觸及：檢查非 storage 欄位衝突與同 slot 寫入衝突。
			baseAcc := layerSnap[addr]
			// balance/code/nonce：任一 fork 改動了該欄位即衝突（無法無損合併）。
			for _, i := range forks {
				facc := touched[i][addr]
				modBal := baseAcc == nil || facc.Balance.Cmp(baseAcc.Balance) != 0
				modCode := baseAcc == nil || !bytes.Equal(facc.Code, baseAcc.Code)
				modNonce := baseAcc == nil || facc.Nonce != baseAcc.Nonce
				if modBal || modCode || modNonce {
					m.world.Revert(sectionSnap)
					return nil, true, nil
				}
			}
			// 同 slot 被 ≥2 fork 寫入 → 衝突。
			for _, writers := range slotWrite[addr] {
				if len(writers) > 1 {
					m.world.Revert(sectionSnap)
					return nil, true, nil
				}
			}
		}

		// 4) 按原序合併回 m.world。
		for _, i := range layer {
			for addr, facc := range touched[i] {
				if len(acctByFork[addr]) > 1 {
					// 共享賬戶（通常是目標合約）：只套用本 fork 相對 layerSnap 的變動 slot。
					baseAcc := layerSnap[addr]
					if baseAcc == nil {
						// 新建共享賬戶——理論上單筆 call 不會新建合約；保守回退。
						m.world.Revert(sectionSnap)
						return nil, true, nil
					}
					for slot, fv := range changedSlots(baseAcc, facc) {
						k, err := decodeStorageSlotKey(slot)
						if err != nil {
							m.world.Revert(sectionSnap)
							return nil, true, err
						}
						m.world.SetStorage(addr, k, fv)
					}
				} else {
					// 私有賬戶：整個賬戶複製（clone 隔離）。
					m.world.accounts[addr] = facc.clone()
				}
			}
		}

		// 5) 持久化（串行、確定性）。
		for _, i := range layer {
			if perr := m.persistState(targets[i]); perr != nil {
				m.world.Revert(sectionSnap)
				return nil, false, fmt.Errorf("vm: 並行持久化失敗: %w", perr)
			}
			results[i] = outs[i].res
		}
	}
	return results, false, nil
}

// decodeStorageSlotKey 把 storage map 的 hex key 還原為 *big.Int（供 SetStorage 重用）。
func decodeStorageSlotKey(keyHex string) (*big.Int, error) {
	b, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	return BytesToInt(b), nil
}
