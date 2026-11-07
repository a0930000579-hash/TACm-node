// Package web 實現 TAC Ai 智能鏈的 Web 層：服務區塊瀏覽、錢包與質押頁面，
// 以 pongo2 渲染、響應式行動優先佈局，數據來自運行中的鏈節點（非靜態頁）。
package web

// StatusView 為鏈網絡概況。
type StatusView struct {
	NodeID          string  `json:"node_id"`
	Address         string  `json:"address"`
	Height          int64   `json:"height"`
	FinalizedHeight int64   `json:"finalized_height"`
	Difficulty      int     `json:"difficulty"`
	MempoolSize     int     `json:"mempool_size"`
	UptimeSec       int64   `json:"uptime_sec"`
	Consensus       string  `json:"consensus"`
	BaseFee         float64 `json:"base_fee"`
	GasPrice        float64 `json:"gas_price"`
	BlockGasLimit   int64   `json:"block_gas_limit"`
	BurnedTotal     float64 `json:"burned_total_tacm"`
}

// TxView 為交易視圖。
type TxView struct {
	Hash        string        `json:"hash"`
	BlockHeight int64         `json:"block_height"`
	TxIndex     int           `json:"tx_index"`
	From        string        `json:"from"`
	To          string        `json:"to"`
	Amount      string        `json:"amount"`
	Fee         string        `json:"fee"`
	Nonce       int64         `json:"nonce"`
	Ts          int64         `json:"ts"`
	Memo        string        `json:"memo"`
	Status      string        `json:"status"`
	Signature   string        `json:"signature"`
	Pubkey      string        `json:"pubkey"`
	Contract    *ContractView `json:"contract,omitempty"`
	GasUsed     int64         `json:"gas_used,omitempty"`
	Logs        []LogView     `json:"logs,omitempty"`
}

// LogView 為交易收據中的事件日誌（M75-2 鏈上 logs 表）。
type LogView struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// ContractView 為合約交易的瀏覽器視圖（memo 為 vm:deploy/vm:call 時非 nil）。
type ContractView struct {
	Kind string `json:"kind"` // deploy / call
	Gas  uint64 `json:"gas"`
	Data string `json:"data"`
}

// BlockView 為區塊視圖（詳情時帶交易）。
type BlockView struct {
	Height          int64    `json:"height"`
	PrevHeight      int64    `json:"prev_height"`
	Hash            string   `json:"hash"`
	PrevHash        string   `json:"prev_hash"`
	MerkleRoot      string   `json:"merkle_root"`
	Proposer        string   `json:"proposer"`
	ProposerAddress string   `json:"proposer_address"`
	Ts              int64    `json:"ts"`
	TxCount         int      `json:"tx_count"`
	Difficulty      int      `json:"difficulty"`
	Nonce           int64    `json:"nonce"`
	Size            int      `json:"size"`
	BaseFee         string   `json:"base_fee,omitempty"`
	GasUsed         int64    `json:"gas_used,omitempty"`
	GasLimit        int64    `json:"gas_limit,omitempty"`
	Burned          string   `json:"burned,omitempty"`
	Transactions    []TxView `json:"transactions"`
}

// AddressView 為地址詳情。
type AddressView struct {
	Address      string   `json:"address"`
	Balance      string   `json:"balance"`
	Nonce        int64    `json:"nonce"`
	Transactions []TxView `json:"transactions"`
}

// WalletView 為錢包頁面視圖（多資產餘額 + 手續費費率 + 帳本流 + TiUSD 供給）。
type WalletView struct {
	Address     string             `json:"address"`
	TACm        string             `json:"tacm"`
	TiUSD       string             `json:"tiusd"`
	USDT        string             `json:"usdt"`
	SyncedBlock int64              `json:"synced_block"`
	FeeTiUSDBps int                `json:"fee_tiusd_bps"`
	FeeUSDTBps  int                `json:"fee_usdt_bps"`
	FeeTACmBps  int                `json:"fee_tacm_bps"`
	Ledger      []WalletLedgerView `json:"ledger"`
	TiUSDSupply int64              `json:"tiusd_supply"`
	TiUSDMinted int64              `json:"tiusd_minted"`
	TiUSDBurned int64              `json:"tiusd_burned"`
	NodeAddress string             `json:"node_address"`
}

// WalletLedgerView 為一條帳本分錄的展示視圖。
type WalletLedgerView struct {
	Ts      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Asset   string `json:"asset"`
	Account string `json:"account"`
	Delta   string `json:"delta"`
	Memo    string `json:"memo"`
}
