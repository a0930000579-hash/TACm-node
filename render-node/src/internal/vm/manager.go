package vm

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const contractSchema = `
CREATE TABLE IF NOT EXISTS evm_contracts (
	address    TEXT PRIMARY KEY,
	code       TEXT NOT NULL,
	creator    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	storage    TEXT NOT NULL,
	balance    TEXT NOT NULL
);`

// ContractInfo 為合約查詢信息。
type ContractInfo struct {
	Address      string `json:"address"`
	CodeSize     int    `json:"code_size"`
	CodeHash     string `json:"code_hash"`
	Creator      string `json:"creator"`
	CreatedAt    int64  `json:"created_at"`
	StorageCount int    `json:"storage_count"`
	Balance      string `json:"balance"`
}

// DeployResult 為部署結果。
type DeployResult struct {
	OK       bool   `json:"ok"`
	Address  string `json:"contract_address,omitempty"`
	GasUsed  uint64 `json:"gas_used"`
	CodeSize int    `json:"code_size,omitempty"`
	Error    string `json:"error,omitempty"`
}

// CallResult 為調用結果。
type CallResult struct {
	OK           bool       `json:"ok"`
	ReturnData   string     `json:"return_data,omitempty"`
	GasUsed      uint64     `json:"gas_used"`
	Logs         []LogEntry `json:"logs,omitempty"`
	Reverted     bool       `json:"reverted"`
	RevertReason string     `json:"revert_reason,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// ContractManager 管理合約代碼/存儲的執行與持久化。
// 價值（value）轉賬由權威鏈賬本（chaindb）的交易結算，管理器只負責
// 執行字節碼並維護合約 code/storage；執行前由節點同步賬戶餘額/nonce。
type ContractManager struct {
	db    *sql.DB
	world *WorldState
	mu    sync.Mutex

	defaultGas uint64
}

// NewContractManager 打開/創建合約庫並加載已部署合約。
func NewContractManager(dbPath string) (*ContractManager, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("vm: 創建合約目錄失敗: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("vm: 打開合約庫失敗: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(contractSchema); err != nil {
		return nil, fmt.Errorf("vm: 初始化合約表失敗: %w", err)
	}
	m := &ContractManager{db: db, world: NewWorldState(), defaultGas: 10_000_000}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// DefaultGas 返回節點預設執行 gas 上限。
func (m *ContractManager) DefaultGas() uint64 { return m.defaultGas }

func (m *ContractManager) load() error {
	rows, err := m.db.Query(
		"SELECT address, code, creator, created_at, storage, balance FROM evm_contracts")
	if err != nil {
		return fmt.Errorf("vm: 讀取合約失敗: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var addr, codeHex, creator, storageJSON, balStr string
		var createdAt int64
		if err := rows.Scan(&addr, &codeHex, &creator, &createdAt, &storageJSON, &balStr); err != nil {
			return fmt.Errorf("vm: 掃描合約行失敗: %w", err)
		}
		code, err := hex.DecodeString(codeHex)
		if err != nil {
			return fmt.Errorf("vm: 合約代碼 hex 錯誤: %w", err)
		}
		m.world.Ensure(addr)
		m.world.SetCode(addr, code)
		if bal, ok := new(big.Int).SetString(balStr, 10); ok {
			m.world.SetBalance(addr, bal)
		}
		var sm map[string]string
		if storageJSON != "" {
			if err := json.Unmarshal([]byte(storageJSON), &sm); err != nil {
				return fmt.Errorf("vm: 合約存儲 JSON 錯誤: %w", err)
			}
			for k, vHex := range sm {
				kb, err := hex.DecodeString(k)
				if err != nil {
					return fmt.Errorf("vm: 存儲鍵 hex 錯誤: %w", err)
				}
				vb, err := hex.DecodeString(vHex)
				if err != nil {
					return fmt.Errorf("vm: 存儲值 hex 錯誤: %w", err)
				}
				m.world.SetStorage(addr, BytesToInt(kb), BytesToInt(vb))
			}
		}
	}
	return rows.Err()
}

func (m *ContractManager) persistNew(addr, creator string, createdAt int64) error {
	acc := m.world.accounts[addr]
	sj, err := m.storageJSON(acc)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`INSERT OR REPLACE INTO evm_contracts
		(address, code, creator, created_at, storage, balance)
		VALUES (?, ?, ?, ?, ?, ?)`,
		addr, hex.EncodeToString(acc.Code), creator, createdAt,
		sj, acc.Balance.String())
	if err != nil {
		return fmt.Errorf("vm: 寫入新合約失敗: %w", err)
	}
	return nil
}

func (m *ContractManager) persistState(addr string) error {
	acc := m.world.accounts[addr]
	sj, err := m.storageJSON(acc)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`UPDATE evm_contracts
		SET code = ?, storage = ?, balance = ? WHERE address = ?`,
		hex.EncodeToString(acc.Code), sj, acc.Balance.String(), addr)
	if err != nil {
		return fmt.Errorf("vm: 更新合約狀態失敗: %w", err)
	}
	return nil
}

func (m *ContractManager) storageJSON(acc *WorldAccount) (string, error) {
	sm := make(map[string]string, len(acc.Storage))
	for k, v := range acc.Storage {
		sm[k] = hex.EncodeToString(IntToBytes(v, WordByteLen))
	}
	b, err := json.Marshal(sm)
	if err != nil {
		return "", fmt.Errorf("vm: 序列化存儲失敗: %w", err)
	}
	return string(b), nil
}

func (m *ContractManager) newCtx(addr, caller, origin string,
	callValue *big.Int) *ExecutionContext {
	return m.newCtxGas(addr, caller, origin, callValue, m.defaultGas)
}

// newCtxGas 建立執行上下文並指定 gas 上限（RPC gas_limit 用）。
func (m *ContractManager) newCtxGas(addr, caller, origin string,
	callValue *big.Int, gas uint64) *ExecutionContext {
	ctx := NewContext()
	ctx.Address = addr
	ctx.Caller = caller
	ctx.Origin = origin
	ctx.CallValue = callValue
	ctx.Gas = gas
	ctx.BlockTimestamp = time.Now().Unix()
	return ctx
}

// SyncAccount 由節點在執行前同步鏈上賬戶餘額/nonce（保留 code/storage）。
func (m *ContractManager) SyncAccount(addr0x string, balance *big.Int, nonce uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.world.Ensure(addr0x)
	if balance != nil {
		m.world.SetBalance(a, balance)
	}
	m.world.accounts[a].Nonce = nonce
}

// runDeploy 為部署核心（不加鎖、不持久化），調用方負責鎖與持久化/回滾。
func (m *ContractManager) runDeploy(addr0x, creator0x string,
	initCode []byte, callValue *big.Int) *DeployResult {
	return m.runDeployWithGas(addr0x, creator0x, initCode, callValue, m.defaultGas)
}

// runDeployWithGas 部署核心（指定 gas，不加鎖、不持久化）。
func (m *ContractManager) runDeployWithGas(addr0x, creator0x string,
	initCode []byte, callValue *big.Int, gas uint64) *DeployResult {
	addr := NormalizeAddress(addr0x)
	creator := NormalizeAddress(creator0x)
	if callValue == nil {
		callValue = new(big.Int)
	}
	m.world.Ensure(addr)

	ctx := m.newCtxGas(addr, creator, creator, callValue, gas)
	interp := NewInterpreter(ctx, m.world, 0, false)
	res := interp.Execute(initCode, nil)

	if res.Reverted {
		return &DeployResult{
			OK: false, GasUsed: res.GasUsed,
			Error: "constructor reverted: " + firstStr(res.Err, res.RevertReason),
		}
	}
	runtime := res.ReturnData
	if len(runtime) == 0 {
		runtime = initCode
	}
	if len(runtime) > MaxCodeSize {
		return &DeployResult{OK: false, Error: ErrCodeTooLarge.Error()}
	}
	m.world.SetCode(addr, runtime)
	return &DeployResult{
		OK: true, Address: addr, GasUsed: res.GasUsed, CodeSize: len(runtime),
	}
}

// ApplyDeploy 執行構造字節碼並持久化運行時代碼（value 已由鏈上交易結算）。
func (m *ContractManager) ApplyDeploy(addr0x, creator0x string,
	initCode []byte, callValue *big.Int) (*DeployResult, error) {
	return m.ApplyDeployGas(addr0x, creator0x, initCode, callValue, m.defaultGas)
}

// ApplyDeployGas 同 ApplyDeploy，但指定 gas 上限（RPC gas_limit 用）。
func (m *ContractManager) ApplyDeployGas(addr0x, creator0x string,
	initCode []byte, callValue *big.Int, gas uint64) (*DeployResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.world.Snapshot()
	r := m.runDeployWithGas(addr0x, creator0x, initCode, callValue, gas)
	if !r.OK {
		m.world.Revert(snap)
		return r, nil
	}
	if err := m.persistNew(r.Address, NormalizeAddress(creator0x),
		time.Now().Unix()); err != nil {
		return nil, err
	}
	return r, nil
}

// SimulateDeploy 只讀模擬部署（快照→執行→回滾，不寫庫）。
func (m *ContractManager) SimulateDeploy(addr0x, creator0x string,
	initCode []byte, callValue *big.Int) (*DeployResult, error) {
	return m.SimulateDeployGas(addr0x, creator0x, initCode, callValue, m.defaultGas)
}

// SimulateDeployGas 同 SimulateDeploy，但指定 gas 上限（RPC gas_limit 用）。
func (m *ContractManager) SimulateDeployGas(addr0x, creator0x string,
	initCode []byte, callValue *big.Int, gas uint64) (*DeployResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.world.Snapshot()
	r := m.runDeployWithGas(addr0x, creator0x, initCode, callValue, gas)
	m.world.Revert(snap)
	return r, nil
}

// runCall 為調用核心（不加鎖、不持久化）。
func (m *ContractManager) runCall(addr0x, caller0x string,
	calldata []byte, callValue *big.Int) *CallResult {
	return m.runCallWithGas(addr0x, caller0x, calldata, callValue, m.defaultGas)
}

// runCallWithGas 調用核心（指定 gas，不加鎖、不持久化）。
func (m *ContractManager) runCallWithGas(addr0x, caller0x string,
	calldata []byte, callValue *big.Int, gas uint64) *CallResult {
	addr := NormalizeAddress(addr0x)
	caller := NormalizeAddress(caller0x)
	if !m.world.HasCode(addr) {
		return &CallResult{OK: false, Error: "contract not deployed"}
	}
	if callValue == nil {
		callValue = new(big.Int)
	}

	ctx := m.newCtxGas(addr, caller, caller, callValue, gas)
	interp := NewInterpreter(ctx, m.world, 0, false)
	res := interp.Execute(m.world.GetCode(addr), calldata)

	if res.Reverted {
		return &CallResult{
			OK: false, Reverted: true, GasUsed: res.GasUsed,
			ReturnData:   hex.EncodeToString(res.ReturnData),
			RevertReason: firstStr(res.Err, res.RevertReason),
		}
	}
	return &CallResult{
		OK: true, GasUsed: res.GasUsed, Logs: res.Logs,
		ReturnData: hex.EncodeToString(res.ReturnData),
	}
}

// ApplyCall 執行已部署合約並持久化存儲。
func (m *ContractManager) ApplyCall(addr0x, caller0x string,
	calldata []byte, callValue *big.Int) (*CallResult, error) {
	return m.ApplyCallGas(addr0x, caller0x, calldata, callValue, m.defaultGas)
}

// ApplyCallGas 同 ApplyCall，但指定 gas 上限（RPC gas_limit 用）。
func (m *ContractManager) ApplyCallGas(addr0x, caller0x string,
	calldata []byte, callValue *big.Int, gas uint64) (*CallResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.world.Snapshot()
	r := m.runCallWithGas(addr0x, caller0x, calldata, callValue, gas)
	if !r.OK {
		m.world.Revert(snap)
		return r, nil
	}
	if err := m.persistState(NormalizeAddress(addr0x)); err != nil {
		return nil, err
	}
	return r, nil
}

// SimulateCall 只讀模擬調用（快照→執行→回滾，不寫庫）。
func (m *ContractManager) SimulateCall(addr0x, caller0x string,
	calldata []byte, callValue *big.Int) (*CallResult, error) {
	return m.SimulateCallGas(addr0x, caller0x, calldata, callValue, m.defaultGas)
}

// SimulateCallGas 同 SimulateCall，但指定 gas 上限（RPC gas_limit 用）。
func (m *ContractManager) SimulateCallGas(addr0x, caller0x string,
	calldata []byte, callValue *big.Int, gas uint64) (*CallResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.world.Snapshot()
	r := m.runCallWithGas(addr0x, caller0x, calldata, callValue, gas)
	m.world.Revert(snap)
	return r, nil
}

// GetCode 返回合約代碼 bytes；不存在回 nil（eth_getCode 用）。
func (m *ContractManager) GetCode(addr0x string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.world.GetCode(NormalizeAddress(addr0x))
}

// Get 返回合約信息；不存在返回 nil。
func (m *ContractManager) Get(addr0x string) *ContractInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := NormalizeAddress(addr0x)
	acc, ok := m.world.accounts[addr]
	if !ok || len(acc.Code) == 0 {
		return nil
	}
	return &ContractInfo{
		Address:      addr,
		CodeSize:     len(acc.Code),
		CodeHash:     "0x" + hex.EncodeToString(Keccak256(acc.Code)),
		StorageCount: len(acc.Storage),
		Balance:      acc.Balance.String(),
	}
}

// List 列出全部合約。
func (m *ContractManager) List() []*ContractInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ContractInfo, 0)
	for addr, acc := range m.world.accounts {
		if len(acc.Code) == 0 {
			continue
		}
		out = append(out, &ContractInfo{
			Address:      addr,
			CodeSize:     len(acc.Code),
			CodeHash:     "0x" + hex.EncodeToString(Keccak256(acc.Code)),
			StorageCount: len(acc.Storage),
			Balance:      acc.Balance.String(),
		})
	}
	return out
}

// Reset 清空合約表與世界狀態（歷史重建前調用）。
func (m *ContractManager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.db.Exec("DELETE FROM evm_contracts"); err != nil {
		return fmt.Errorf("vm: 清空合約表失敗: %w", err)
	}
	m.world = NewWorldState()
	return nil
}

// StorageAt 返回合約某槽值（只讀，加鎖）。
func (m *ContractManager) StorageAt(addr0x string, key *big.Int) *big.Int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.world.GetStorage(addr0x, key)
}

// Close 關閉數據庫。
func (m *ContractManager) Close() error { return m.db.Close() }

func firstStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
