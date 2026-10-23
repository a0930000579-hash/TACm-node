package node

import (
	"fmt"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/vm"
)

// 本檔把區塊合約執行改為「並行優先、串行兜底」：
// 把連續的純 vm:call 合格交易切成可並行段落，呼叫 ContractManager.ApplyCallsParallel
// 樂觀並行執行；一旦 fellBack、任一結果 !OK 或任何錯誤，整段改用既有 applyContractTx
// 逐筆串行重放，以得到與舊行為逐字一致的結果與第一個失敗點。
// deploy、非合約交易與靜態不合格呼叫一律打斷段落、走原 applyContractTx（行為不變）。

// parallelCallEligible 判斷某筆交易是否可進入並行段落：必須是可解碼的 vm:call、
// 目標為已部署合約、且合約位元組碼不含 CALL/CREATE-family opcode。
func (n *Node) parallelCallEligible(t *chaindb.Transaction) bool {
	if !strings.HasPrefix(t.Memo, vmCallPrefix) {
		return false
	}
	_, payload, err := splitContractMemo(t.Memo)
	if err != nil {
		return false
	}
	calldata, err := hexInput(payload)
	if err != nil {
		return false
	}
	to0x, err := tx0To0x(t.ToAddr)
	if err != nil {
		return false
	}
	from0x, err := tx0To0x(t.FromAddr)
	if err != nil {
		return false
	}
	code := n.contracts.GetCode(to0x)
	elig, _ := vm.CallParallelEligible(code, from0x, to0x, calldata)
	return elig
}

// flushParallelSection 把一段「連續合格 vm:call」併行執行；任何不確定即整段串行重放。
func (n *Node) flushParallelSection(section []*chaindb.Transaction) error {
	if len(section) == 0 {
		return nil
	}
	// chaindb 已結算：先對本段落所有 from/to 同步鏈上餘額/nonce（等價串行逐筆同步）。
	for _, t := range section {
		if err := n.syncChainAccount(t.FromAddr); err != nil {
			return err
		}
		if err := n.syncChainAccount(t.ToAddr); err != nil {
			return err
		}
	}

	items := make([]vm.ParallelCallItem, 0, len(section))
	for _, t := range section {
		gas, payload, err := splitContractMemo(t.Memo)
		if err != nil {
			return err
		}
		calldata, err := hexInput(payload)
		if err != nil {
			return fmt.Errorf("node: calldata 錯誤: %w", err)
		}
		to0x, err := tx0To0x(t.ToAddr)
		if err != nil {
			return err
		}
		from0x, err := tx0To0x(t.FromAddr)
		if err != nil {
			return err
		}
		items = append(items, vm.ParallelCallItem{
			To0x: to0x, From0x: from0x,
			Calldata: calldata, Value: parseAmount(t.Amount), Gas: gas,
		})
	}

	results, fellBack, err := n.contracts.ApplyCallsParallel(items)
	if err != nil {
		return err
	}
	if fellBack {
		// 整段串行重放（world 已回滾到段落前）。
		for _, t := range section {
			if rerr := n.applyContractTx(t, true); rerr != nil {
				return rerr
			}
		}
		return nil
	}

	// 並行成功：依原序把各筆結果寫回 logs 與 gas（與 applyContractTx 成功分支一致）。
	for i, t := range section {
		cr := results[i]
		if cr == nil || !cr.OK {
			// 理論上 ApplyCallsParallel 已 fellBack；保險起見仍回退串行。
			for _, tt := range section {
				if rerr := n.applyContractTx(tt, true); rerr != nil {
					return rerr
				}
			}
			return nil
		}
		if err := n.persistContractLogs(t, cr.Logs); err != nil {
			return err
		}
		if cr.GasUsed > 0 {
			if err := n.db.UpdateTxGasUsed(t.TxHash, int64(cr.GasUsed)); err != nil {
				return err
			}
		}
	}
	return nil
}

// executeBlockContractsParallel 在區塊插入後執行其中的合約交易：
// 連續合格 vm:call 走並行段落；deploy / 非合約 / 不合格呼叫打斷並逐筆串行。
func (n *Node) executeBlockContractsParallel(txs []chaindb.Transaction) error {
	var section []*chaindb.Transaction
	flush := func() error {
		if len(section) == 0 {
			return nil
		}
		err := n.flushParallelSection(section)
		section = section[:0]
		return err
	}

	for i := range txs {
		t := &txs[i]
		if n.parallelCallEligible(t) {
			section = append(section, t)
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if err := n.applyContractTx(t, true); err != nil {
			return err
		}
	}
	return flush()
}
