package chaindb

import (
	"encoding/hex"
	"fmt"

	"tacm/internal/crypto"
)

// CoinbaseTxHash 計算 coinbase 交易的確定性哈希（coinbase 無簽名，
// 由高度、提議者、獎勵與時間唯一確定）。
func CoinbaseTxHash(height int64, proposer string, reward float64, ts int64) string {
	canonical := fmt.Sprintf("tacm:coinbase:%d:%s:%s:%d",
		height, proposer, FormatFloat(reward), ts)
	return hex.EncodeToString(crypto.DoubleSHA256([]byte(canonical)))
}

// BuildCoinbaseTx 構造區塊的 coinbase 增發交易（from 為空，僅入賬不扣款）。
// height=0（創世）或無提議者時返回獎勵為 0 的空交易。
func BuildCoinbaseTx(height int64, proposerAddr string, ts int64) Transaction {
	reward := BlockReward(height)
	return Transaction{
		TxHash:   CoinbaseTxHash(height, proposerAddr, reward, ts),
		FromAddr: "", ToAddr: proposerAddr,
		Amount: FormatFloat(reward), Fee: "0", Nonce: 0, Ts: ts,
		Memo: "coinbase", Status: "confirmed",
	}
}

// RewardPoolAddr 鏈上獎勵池帳戶（與 wallet.RewardPoolAddr 同值，避免包循環引用）。
const RewardPoolAddr = "reward_pool"

// GovTreasuryTxHash 計算治理 treasury 撥款交易的確定性哈希（M151c）。
// 撥款由出塊引擎依治理引擎到期提案建單（無簽名、全節點照單執行，語意同 coinbase）。
func GovTreasuryTxHash(height int64, proposalID int64, to string, amount float64, ts int64) string {
	canonical := fmt.Sprintf("tacm:govtreasury:%d:%d:%s:%s:%d",
		height, proposalID, to, FormatFloat(amount), ts)
	return hex.EncodeToString(crypto.DoubleSHA256([]byte(canonical)))
}

// PoolShareBps 每區塊 coinbase 挹注獎勵池的比例（15% = 1500 bp）。
// M149o：節點 45%（NodeShareBps，PoT 在線節點均分）＋池 15%＋礦工 40%（MinerShareBps），恆等式＝100%。
const PoolShareBps = 1500

// NodeShareBps 每區塊 coinbase 給節點的獎勵比例（45% = 4500 bp）——由鏈上在線 PoT 節點均分，「節點收益歸節點」。
const NodeShareBps = 4500

// MinerShareBps 每區塊 coinbase 分給在線礦工瓜分的比例（40% = 4000 bp）。
const MinerShareBps = 4000

// CoinbaseSplit 出塊獎勵瓜分份額（Share 為算力整數，參與按算力比例分配）。
type CoinbaseSplit struct {
	Address string
	Share   int64
}

// CoinbaseSplitTxHash 計算瓜分 coinbase 交易的確定性哈希（含序號與收款人，避免多筆衝突）。
func CoinbaseSplitTxHash(height int64, idx int, to string, amount float64, ts int64) string {
	canonical := fmt.Sprintf("tacm:coinsplit:%d:%d:%s:%s:%d",
		height, idx, to, FormatFloat(amount), ts)
	return hex.EncodeToString(crypto.DoubleSHA256([]byte(canonical)))
}

// BuildCoinbaseTxs 構造出塊獎勵的鏈上瓜分交易清單（M58：瓜分明細寫入區塊，全網一致）。
// reward 的 PoolShareBps（15%）進 reward_pool；其餘按 splits 算力比例分給在線礦工；
// 無在線礦工時其餘全數進 reward_pool（維持 M36 語義，避免「未開機卻進帳」）。
// 回傳一或多筆 from 為空的 coinbase 交易；所有節點照單執行 → 收益/獎勵池全網一致。
func BuildCoinbaseTxs(height int64, proposerAddr string, ts int64, splits []CoinbaseSplit, reward float64, onlineNodes ...string) []Transaction {
	if reward <= 1e-9 {
		return nil
	}
	// M70：每塊 coinbase 拆三份——節點 45%（coinbase:node，PoT 在線節點均分，節點收益歸節點）
	// ＋池 15%（coinbase:pool，自動挹注獎勵池）＋礦工 40%（coinbase:miner，按在線算力瓜分）。
	// 恆等式：45%＋15%＋40%＝100%，任何節點照單執行 → 全網帳本一致，A+B+C＝實際總產出。
	poolShare := reward * float64(PoolShareBps) / 10000
	nodeShare := reward * float64(NodeShareBps) / 10000
	rest := reward - poolShare - nodeShare
	out := []Transaction{}
	add := func(to string, amt float64, tag string) {
		if amt <= 1e-9 {
			return
		}
		out = append(out, Transaction{
			TxHash:   CoinbaseSplitTxHash(height, len(out), to, amt, ts),
			FromAddr: "", ToAddr: to,
			Amount: FormatFloat(amt), Fee: "0", Nonce: 0, Ts: ts,
			Memo: "coinbase:" + tag, Status: "confirmed",
		})
	}
	// M149m-7：節點獎勵（45%）由「鏈上在線節點」瓜分（PoT 時間權證）——
	// follower 節點定期發 hb:on:0 心跳即入在線節點集，不再只有出塊者獨享；
	// 無在線節點時回退給出塊節點，再無地址時併入獎勵池（恆等式不破）。
	if len(onlineNodes) > 0 {
		per := nodeShare / float64(len(onlineNodes))
		for _, addr := range onlineNodes {
			if per <= 1e-9 {
				break
			}
			add(addr, per, "node")
		}
	} else if proposerAddr != "" {
		add(proposerAddr, nodeShare, "node")
	} else {
		// 極端防護：無節點地址時 45% 併入獎勵池，恆等式不破。
		add(RewardPoolAddr, nodeShare, "reserve")
	}
	var totalShare int64
	for _, sp := range splits {
		if sp.Share > 0 {
			totalShare += sp.Share
		}
	}
	if totalShare <= 0 {
		// 無在線礦工：40% 也進獎勵池（M36 語義）。
		add(RewardPoolAddr, rest, "reserve")
		add(RewardPoolAddr, poolShare, "pool")
		return out
	}
	var acc float64
	n := 0
	for _, sp := range splits {
		if sp.Share <= 0 {
			continue
		}
		n++
	}
	idx := 0
	for _, sp := range splits {
		if sp.Share <= 0 {
			continue
		}
		idx++
		var portion float64
		if idx == n {
			portion = rest - acc
		} else {
			portion = rest * float64(sp.Share) / float64(totalShare)
			acc += portion
		}
		add(sp.Address, portion, "miner")
	}
	add(RewardPoolAddr, poolShare, "pool")
	return out
}
