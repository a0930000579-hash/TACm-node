package rollup

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	"tacm/internal/crypto"
)

// TreeSnapshot 為狀態樹快照（賬戶地址 → 賬戶值）。
type TreeSnapshot struct {
	Accounts map[string]L2Account
}

// StateTree 為 L2 賬戶狀態樹：鍵值賬戶 → 狀態 Merkle 根。
type StateTree struct {
	accounts map[string]*L2Account
}

// NewStateTree 建立空狀態樹。
func NewStateTree() *StateTree {
	return &StateTree{accounts: map[string]*L2Account{}}
}

func (t *StateTree) ensure(addr string) *L2Account {
	a, ok := t.accounts[addr]
	if !ok {
		a = &L2Account{Address: addr, StorageRoot: ZeroRoot}
		t.accounts[addr] = a
	}
	return a
}

// SetAccount 寫入賬戶（值拷貝）。
func (t *StateTree) SetAccount(a L2Account) {
	if a.StorageRoot == "" {
		a.StorageRoot = ZeroRoot
	}
	cp := a
	t.accounts[a.Address] = &cp
}

// GetAccount 取賬戶；不存在返回零值賬戶與 false。
func (t *StateTree) GetAccount(addr string) (L2Account, bool) {
	a, ok := t.accounts[addr]
	if !ok {
		return L2Account{Address: addr, StorageRoot: ZeroRoot}, false
	}
	return *a, true
}

// Balance 返回地址 L2 餘額。
func (t *StateTree) Balance(addr string) float64 {
	if a, ok := t.accounts[addr]; ok {
		return a.Balance
	}
	return 0
}

// AccountCount 返回狀態樹賬戶數。
func (t *StateTree) AccountCount() int { return len(t.accounts) }

func (t *StateTree) addBalance(addr string, delta float64) {
	t.ensure(addr).Balance += delta
}

func (t *StateTree) incNonce(addr string) { t.ensure(addr).Nonce++ }

// accountLeaf 為單個賬戶的狀態葉子（確定序列化，float 最短往返）。
func accountLeaf(a L2Account) string {
	s := fmt.Sprintf("%s:%s:%d:%s",
		a.Address,
		strconv.FormatFloat(a.Balance, 'g', -1, 64),
		a.Nonce, a.StorageRoot)
	return hex.EncodeToString(crypto.DoubleSHA256([]byte(s)))
}

// ComputeRoot 計算全部賬戶狀態 Merkle 根（按地址排序）；空樹返回 ZeroRoot。
func (t *StateTree) ComputeRoot() (string, error) {
	if len(t.accounts) == 0 {
		return ZeroRoot, nil
	}
	addrs := make([]string, 0, len(t.accounts))
	for a := range t.accounts {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	leaves := make([]string, 0, len(addrs))
	for _, a := range addrs {
		leaves = append(leaves, accountLeaf(*t.accounts[a]))
	}
	return crypto.MerkleRootStrings(leaves)
}

// Snapshot 深拷貝全部賬戶狀態。
func (t *StateTree) Snapshot() *TreeSnapshot {
	snap := &TreeSnapshot{Accounts: map[string]L2Account{}}
	for k, a := range t.accounts {
		snap.Accounts[k] = *a
	}
	return snap
}

// Restore 從快照恢復（替換當前全部狀態）。
func (t *StateTree) Restore(snap *TreeSnapshot) {
	t.accounts = map[string]*L2Account{}
	if snap == nil {
		return
	}
	for k, a := range snap.Accounts {
		cp := a
		if cp.StorageRoot == "" {
			cp.StorageRoot = ZeroRoot
		}
		t.accounts[k] = &cp
	}
}

// applyOnTree 在指定樹上執行一筆交易賬務（不持久化）；
// 發送方不存在或餘額不足返回 false（不改動）。
func applyOnTree(t *StateTree, tx *L2Transaction) bool {
	sender, ok := t.GetAccount(tx.FromAddr)
	if !ok || sender.Balance < tx.Amount+tx.Fee {
		return false
	}
	t.addBalance(tx.FromAddr, -(tx.Amount + tx.Fee))
	t.addBalance(tx.ToAddr, tx.Amount)
	t.incNonce(tx.FromAddr)
	return true
}
