package vm

import (
	"encoding/hex"
	"math/big"
)

// WorldAccount 為世界狀態中的賬戶（EOA 或合約）。
type WorldAccount struct {
	Balance *big.Int
	Code    []byte
	Storage map[string]*big.Int // key = 32 字節 hex（*big.Int 不可作 map key）
	Nonce   uint64
}

func newAccount() *WorldAccount {
	return &WorldAccount{Balance: new(big.Int), Storage: map[string]*big.Int{}}
}

func (a *WorldAccount) clone() *WorldAccount {
	c := &WorldAccount{
		Balance: new(big.Int).Set(a.Balance),
		Code:    append([]byte(nil), a.Code...),
		Nonce:   a.Nonce,
		Storage: map[string]*big.Int{},
	}
	for k, v := range a.Storage {
		c.Storage[k] = new(big.Int).Set(v)
	}
	return c
}

func storageKey(k *big.Int) string { return hex.EncodeToString(IntToBytes(k, WordByteLen)) }

// WorldState 為 EVM 世界狀態：規範地址 → 賬戶。
// 合約間消息調用在同一狀態上遞歸執行；子調用前快照，失敗回滾、成功保留。
// 約定：一次交易執行在單個 goroutine 內順序訪問（EVM 交易串行語義）。
type WorldState struct {
	accounts map[string]*WorldAccount
}

func NewWorldState() *WorldState {
	return &WorldState{accounts: map[string]*WorldAccount{}}
}

// Ensure 確保賬戶存在，返回規範地址。
func (s *WorldState) Ensure(addr any) string {
	a := NormalizeAddress(addr)
	if _, ok := s.accounts[a]; !ok {
		s.accounts[a] = newAccount()
	}
	return a
}

func (s *WorldState) GetBalance(addr any) *big.Int {
	a := NormalizeAddress(addr)
	if acc, ok := s.accounts[a]; ok {
		return new(big.Int).Set(acc.Balance)
	}
	return new(big.Int)
}

func (s *WorldState) AddBalance(addr any, delta *big.Int) {
	a := s.Ensure(addr)
	acc := s.accounts[a]
	acc.Balance = U256(new(big.Int).Add(acc.Balance, delta))
}

func (s *WorldState) SetBalance(addr any, value *big.Int) {
	a := s.Ensure(addr)
	s.accounts[a].Balance = U256(value)
}

func (s *WorldState) GetCode(addr any) []byte {
	a := NormalizeAddress(addr)
	if acc, ok := s.accounts[a]; ok {
		return append([]byte(nil), acc.Code...)
	}
	return nil
}

func (s *WorldState) SetCode(addr any, code []byte) {
	a := s.Ensure(addr)
	s.accounts[a].Code = append([]byte(nil), code...)
}

func (s *WorldState) GetStorage(addr any, key *big.Int) *big.Int {
	a := NormalizeAddress(addr)
	if acc, ok := s.accounts[a]; ok {
		if v, ok2 := acc.Storage[storageKey(key)]; ok2 {
			return new(big.Int).Set(v)
		}
	}
	return new(big.Int)
}

func (s *WorldState) SetStorage(addr any, key, value *big.Int) {
	a := s.Ensure(addr)
	s.accounts[a].Storage[storageKey(key)] = U256(value)
}

func (s *WorldState) GetNonce(addr any) uint64 {
	a := NormalizeAddress(addr)
	if acc, ok := s.accounts[a]; ok {
		return acc.Nonce
	}
	return 0
}

func (s *WorldState) IncNonce(addr any) {
	a := s.Ensure(addr)
	s.accounts[a].Nonce++
}

func (s *WorldState) HasCode(addr any) bool { return len(s.GetCode(addr)) > 0 }

// Transfer 轉賬：校驗餘額，成功扣款並收款，失敗返回 false（不變動）。
func (s *WorldState) Transfer(from, to any, value *big.Int) bool {
	if value.Sign() < 0 {
		return false
	}
	f := NormalizeAddress(from)
	r := NormalizeAddress(to)
	if s.GetBalance(f).Cmp(value) < 0 {
		return false
	}
	s.AddBalance(f, new(big.Int).Neg(value))
	s.AddBalance(r, value)
	return true
}

// Snapshot 深拷貝全部賬戶。
func (s *WorldState) Snapshot() map[string]*WorldAccount {
	out := make(map[string]*WorldAccount, len(s.accounts))
	for a, acc := range s.accounts {
		out[a] = acc.clone()
	}
	return out
}

// Revert 回滾到快照。
func (s *WorldState) Revert(snap map[string]*WorldAccount) { s.accounts = snap }
