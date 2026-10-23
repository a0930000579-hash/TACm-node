// Package node 實現 TAC Ai 智能鏈的單節點：持續出塊、交易驗證與內存池、
// 動態難度，以及輕客戶端所需的查詢能力。M2 為單節點垂直切片，P2P 網絡與
// BFT 最終性在後續里程碑接入。
package node

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"tacm/internal/aa"
	"tacm/internal/aimarket"
	"tacm/internal/aiverify"
	bridgepkg "tacm/internal/bridge"
	"tacm/internal/c2c"
	"tacm/internal/chaindb"
	"tacm/internal/community"
	"tacm/internal/config"
	"tacm/internal/consensus/bft"
	"tacm/internal/consensus/difficulty"
	"tacm/internal/consensus/pos"
	"tacm/internal/crypto"
	"tacm/internal/defi"
	"tacm/internal/dex"
	"tacm/internal/dexv3"
	"tacm/internal/ens"
	"tacm/internal/exchange"
	"tacm/internal/governance"
	"tacm/internal/liquidstake"
	"tacm/internal/oracle"
	"tacm/internal/orderbook"
	"tacm/internal/p2p"
	"tacm/internal/perp"
	"tacm/internal/rollup"
	"tacm/internal/stateprune"
	"tacm/internal/vm"
	"tacm/internal/wallet"
)

const (
	maxTxPerBlock = 500
	fastBlockTime = 1 * time.Second
)

// errNotProposer 表示當前高度不由本節點提議（分佈式模式下靜默等待網絡塊）。
var errNotProposer = errors.New("not my turn to propose")

// Node 為 TAC Ai 智能鏈節點。
type Node struct {
	cfg    *config.Config
	db     *chaindb.ChainDB
	nodeID string

	keypair     *crypto.KeyPair
	nodeAddress string

	blockTime   time.Duration
	onlineSince time.Time

	// heartbeatInterval 礦工鏈上心跳續命間隔（M61：所有啟動模式皆跑 minerHeartbeatLoop；
	// 預設 hbRefresh，測試可注入較短間隔以快速驗證自動續命）。
	heartbeatInterval time.Duration

	// tacnsAddr P1-① TAC-ENS：本節點部署的 TACNames 註冊表地址（0x 格式，記憶體記錄）。
	tacnsAddr string

	// tacwalletAddr P1-② 多簽合約錢包：本節點部署的 TACWallet 地址（0x 格式，記憶體記錄）。
	tacwalletAddr string
	// tacgovAddr P1-③ 鏈上治理：本節點部署的 TACGovernor 地址（0x 格式，記憶體記錄）。
	tacgovAddr string
	// govProposalCount 記憶體記錄已 propose 的提案數（供 status 展示下一個 pid）。
	govProposalCount int64
	// walletOwners 記憶體記錄已 addOwner 的 owner（0x 格式，供 Explorer 面板展示；
	// 重啟後以鏈上 ownerCount/isOwner 為準）。
	walletOwners []string

	mu       sync.Mutex // 保護以下共識狀態
	baseDiff int
	adjDiff  int

	running     bool
	distributed bool // 分佈式 BFT：僅輪值 proposer 出塊，其餘投票
	stopCh      chan struct{}
	wg          sync.WaitGroup

	// consensusWake 喚醒 consensusLoop（收到 view-change / 新提案時立即重試）。
	consensusWake chan struct{}

	// BFT 即時最終性
	vset        *bft.ValidatorSet
	bftNode     *bft.BFTNode
	finalityLog *bft.FinalityLog

	// M140 合約 ABI/Source 驗證元資料（data-dir 持久化）。
	metaMu         sync.Mutex
	verifyMeta     map[string]ContractMeta
	metaPath       string
	finalizedH     int64
	precommitMu    sync.Mutex
	precommitVotes map[int64]map[string]bft.Vote

	// v54：失聯驗證人接管——輪值 proposer 提議超時（創世節點/主鏈掛機）時，
	// 在線驗證人接管該高度出塊，鏈不中斷（參照比特幣：部分礦工離線其餘照挖）。
	takeoverMu     sync.Mutex
	takeover       map[int64]bool
	takeoverActive bool // 接管狀態持續生效（後續高度免等待），收到網絡塊後清除

	// M13 錢包帳本（多資產：TACm/TiUSD/USDT；隨鏈上區塊同步）。
	walletSvc    *wallet.Service
	exchangeSvc  *exchange.Service
	communitySvc *community.Store
	defiSvc      *defi.Store
	ensSvc       *ens.Store
	c2cSvc       *c2c.Store
	dexv3St      *dexv3.Store
	perpSt       *perp.Store
	aimSt        *aimarket.Store
	aivSt        *aiverify.Store
	obSt         *orderbook.Store
	pruner       *stateprune.Pruner
	authSvc      *AuthService

	// M40 鏈上治理（提案/投票/參數執行；高度驅動）。
	gov *governance.Governance
	ls  *liquidstake.LiquidStake

	// v129 鏈上多簽錢包（Gnosis Safe 模式；N-of-M 門檻批准後執行）。
	ms *MultisigManager

	// v130 鏈上拍賣（Auction；掛單/競價/結算）。
	auc *AuctionManager

	// M12 view-change 硬化：多數認證票集（>2/3 驗證人簽名才切輪）。
	vcMu         sync.Mutex
	viewChangeTk *viewChangeTickets

	// P2P 網絡（可選；nil 時本節點不廣播，作為孤立節點運行）
	p2pNet *p2p.Network

	// 智能合約執行層（TAC VM）
	contracts *vm.ContractManager

	// 鏈上 DEX（AMM 恆定乘積交易所；nil=合約層未啟用時不初始化）
	dex *dex.Engine

	// M87 動態驗證人集：PoS 質押層（pos.db）+ 雙簽罰沒追蹤器 + 週期重建。
	posStore     *pos.Store
	slashTracker *bft.SlashingTracker

	// M152 鏈上預言機：節點級喂價服務（外部行情源 + 手動覆寫 + 歷史）。
	oracleSvc *oracle.Oracle

	// M154 帳戶抽象（EIP-4337）：離線 EntryPoint/Bundler——驗證 UserOperation 授權後代執行。
	aaSvc *aa.AA

	// 官方代簽的 mempool pending nonce（同 from 連續多筆合約交易佔用 nonce；
	// 入塊後 db.GetNonce 已含全部，resetPendingTx 清空）。
	txNonceMu      sync.Mutex
	txNoncePending map[string]int64

	// DEX 池簽名對映（poolID → token0:token1；建池時記錄，供簽名派生）。
	dexPoolMu   sync.Mutex
	dexPoolKeys map[string]string

	// Layer2 Optimistic Rollup（可選；nil=不啟用）
	l2    *rollup.Rollup
	l2svc *rollup.Service

	// 跨鏈橋（可選；nil=不啟用）
	bridge          *bridgepkg.Bridge
	relays          map[string]*bridgepkg.Relayer // M150h：多鏈中繼實例（chainName→Relayer；bsc 向後相容）
	relayCancels    map[string]context.CancelFunc // M150h：多鏈中繼取消（chainName→CancelFunc）
	bridgeNetworked bool                          // 已配置守衛網絡（跨鏈提案經 P2P 傳播聚合多簽）
}

// New 創建節點：打開數據庫、重放動態難度、載入或生成節點密鑰。
func New(cfg *config.Config, nodeID string, baseDifficulty int) (*Node, error) {
	if cfg == nil {
		return nil, errors.New("node: 配置為空")
	}
	dataDir := cfg.DataDir
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("node: 創建數據目錄失敗: %w", err)
	}
	db, err := chaindb.Open(filepath.Join(dataDir, "chain.db"))
	if err != nil {
		return nil, err
	}

	bt := time.Duration(cfg.BlockTime) * time.Second
	if bt <= 0 {
		bt = 3 * time.Second
	}

	n := &Node{
		cfg:               cfg,
		db:                db,
		nodeID:            nodeID,
		blockTime:         bt,
		baseDiff:          baseDifficulty,
		adjDiff:           baseDifficulty,
		stopCh:            make(chan struct{}),
		onlineSince:       time.Now(),
		heartbeatInterval: hbRefresh,
		verifyMeta:        map[string]ContractMeta{},
		metaPath:          filepath.Join(dataDir, "contracts_meta.json"),
		takeover:          map[int64]bool{},
	}
	n.loadContractMeta()

	// 錢包帳本（隨鏈同步；獨立於鏈庫的事務帳本）。
	ws, err := wallet.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.walletSvc = wallet.NewService(ws)
	n.syncGenesisAlloc() // M118：創世分配同步進 wallet 帳本（修正餘額不足/跳號根因）
	// M35：TiUSD 錨定——注入 TACm 總供應上限（TACM_MAX_SUPPLY），流通量低於 0.330 倍時自動補鑄至下限。
	if ecfg, eerr := chaindb.LoadEmission(int64(cfg.BlockTime)); eerr == nil && ecfg.Model == "annual_decay" && ecfg.MaxSupply > 0 {
		n.walletSvc.SetTACMSupplyCap(ecfg.MaxSupply)
	}
	if _, ferr := n.walletSvc.EnsureTiUSDFloor(); ferr != nil {
		_ = db.Close()
		return nil, ferr
	}

	// 交易所撮合引擎（獨立帳本：ex_balances/ex_orders/ex_trades）。
	es, err := exchange.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.exchangeSvc = exchange.NewService(es)

	// 內建社群（FB 風動態牆/市集/廣告）：community.db。
	cs, err := community.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.communitySvc = cs

	// M40 鏈上治理：governance.db（提案/投票/參數執行）。
	gs, gerr := governance.New(filepath.Join(dataDir, "governance.db"))
	if gerr != nil {
		_ = db.Close()
		return nil, gerr
	}
	gs.SetApplyFunc(n.applyGovernanceParam)
	n.gov = gs
	// v129 鏈上多簽錢包：multisig.db（Gnosis Safe 模式）。
	ms, merr := NewMultisigManager(filepath.Join(dataDir, "multisig.db"))
	if merr != nil {
		_ = db.Close()
		return nil, merr
	}
	n.ms = ms
	// v130 鏈上拍賣：auction.db（掛單/競價/結算）。
	am, aerr := NewAuctionManager(filepath.Join(dataDir, "auction.db"))
	if aerr != nil {
		_ = db.Close()
		return nil, aerr
	}
	n.auc = am
	// v73 流動性質押：liquidstake.db（存款→stTACm 憑證，匯率制）。
	ls, lserr := liquidstake.New(filepath.Join(dataDir, "liquidstake.db"))
	if lserr != nil {
		_ = db.Close()
		return nil, lserr
	}
	n.ls = ls
	// M151c-v2：重啟後套用既有治理參數——手續費預留同步至錢包帳本。
	if n.walletSvc != nil {
		n.walletSvc.SetTreasuryFeeBps(int(gs.Config().TreasuryFeeBps))
	}

	// DeFi（流動性挖礦＋借貸市場）：defi.db，資產對接 wallet 帳本。
	ds, err := defi.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.defiSvc = ds
	// 池/借貸市場初始流動性由鏈上基金注資（Reward 鑄造、僅在帳戶為空時執行，冪等）。
	if err := n.seedDefiLiquidity(); err != nil {
		_ = db.Close()
		return nil, err
	}

	// ENS 鏈上域名（.tac）：ens.db。
	ensSt, err := ens.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.ensSvc = ensSt

	// C2C 場外交易：c2c.db。
	cs2, err := c2c.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.c2cSvc = cs2

	// DEX V3 集中流動性（對標 Uniswap V3）：dexv3.db。
	dexv3St, err := dexv3.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.dexv3St = dexv3St

	// AI 模型市場（對標 Bittensor/Ritual）：aimarket.db。
	aimSt, err := aimarket.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("aimarket open: %w", err)
	}
	n.aimSt = aimSt

	// 鏈上訂單簿式現貨交易（對標 Binance orderbook；公開可驗證、鏈上託管）：orderbook.db。
	obSt, err := orderbook.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("orderbook open: %w", err)
	}
	n.obSt = obSt

	// 跨鏈 AI 推理驗證（對標 Bittensor/Ritual 驗證網路）：aiverify.db。
	aivSt, err := aiverify.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("aiverify open: %w", err)
	}
	n.aivSt = aivSt

	// 永續合約（對標 dYdX/GMX）：perp.db；標記價格源接 oracle。
	perpSt, err := perp.Open(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("perp open: %w", err)
	}
	perpSt.SetPriceFn(func(sym string) (float64, bool) {
		if n.oracleSvc == nil {
			return 0, false
		}
		p, ok := n.oracleSvc.Get(sym)
		return p.Price, ok
	})
	n.perpSt = perpSt

	// 會員系統（註冊/登入/登出）：users.db。
	as, err := OpenAuth(dataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	n.authSvc = as

	// 節點密鑰：持久化於 node_key.json，否則新生成。
	if err := n.loadOrCreateKey(dataDir); err != nil {
		db.Close()
		return nil, err
	}
	// 節點自身註冊為默認礦機（算力 1 vCPU）：保證單節點下 coinbase 剩餘有歸屬。
	// 對齊原本方式：出塊補貼扣除獎勵池份額後，按在線礦工算力瓜分。
	if err := n.walletSvc.Store().RegisterMiner(n.nodeAddress, 1, 0); err != nil {
		db.Close()
		return nil, err
	}

	// BFT 驗證人：以本節點密鑰作為共識密鑰（單機驗證人集）。
	if err := n.initBFT(); err != nil {
		db.Close()
		return nil, err
	}

	// 動態難度：從鏈上歷史時間戳重放當前基礎難度。
	tip := db.GetTipHeight()
	n.adjDiff = difficulty.ReplayDifficulty(n.blockLookup, tip,
		baseDifficulty, bt.Seconds(), difficulty.RetargetInterval)

	// M87 PoS 質押層（pos.db）：動態驗證人集的資料來源。開啟失敗不致命——
	// 共識回退創世驗證人集（單節點仍可正常出塊）。
	if ps, perr := pos.Open(filepath.Join(dataDir, "pos.db"), pos.DefaultConfig()); perr == nil {
		n.posStore = ps
		n.slashTracker = bft.NewSlashingTracker(pos.DefaultConfig().SlashRate * pos.DefaultConfig().ValidatorMinStk)
	} else {
		n.posStore = nil
	}

	// 智能合約管理器：持久化於 tac_evm.db。
	cm, err := vm.NewContractManager(filepath.Join(dataDir, "tac_evm.db"))
	if err != nil {
		db.Close()
		return nil, err
	}
	n.contracts = cm

	// 鏈上 DEX（AMM）：以節點金鑰做官方做市、池帳戶以 DeriveKey 派生；
	// 合約層未啟用時 dex 為 nil（RPC 回「未啟用」）。
	if e, derr := dex.New(dataDir, n.nodeAddress, n.dexTransfer, n.dexBalance); derr == nil {
		n.dex = e
	}

	// 可選：啟用 Layer2 Optimistic Rollup（含後台批量/提交服務）。
	if cfg.EnableL2 {
		if err := n.AttachL2(); err != nil {
			_ = cm.Close()
			_ = db.Close()
			return nil, err
		}
	}

	// 可選：啟用跨鏈橋。
	if cfg.EnableBridge {
		if err := n.AttachBridge(); err != nil {
			_ = cm.Close()
			_ = db.Close()
			return nil, err
		}
	}

	// v102：TiUSD 市值錨定——TACm 市價注入（訂單簿最後成交價）＋市值上限自動銷毀。
	// 無成交（未上市）時市價維持 0 → 上限按 1:0.330 兜底。
	go n.priceCapLoop()

	// v156 A1：狀態膨脹治理背景排程（保守預設：保留 50 萬塊、不動 live state、預設不自動 VACUUM），
	// 週期清理/歸檔最終化歷史 blocks/txs/logs，預防 chain.db 無限膨脹導致 OOM 斷線。
	n.pruner = stateprune.New(n.db, stateprune.Config{
		Interval: time.Hour,
		Prune:    chaindb.DefaultPruneConfig(),
	})

	return n, nil
}

// priceCapLoop v102：每 30 秒注入 TACm 市價（TACm/USDT 最後成交價）並執行
// TiUSD 市值上限自動銷毀（流通 > 市值 → 銷毀至市值，確保不超額膨脹）。
func (n *Node) priceCapLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		if n.walletSvc == nil {
			continue
		}
		if n.exchangeSvc != nil {
			m := exchange.Market{Base: exchange.AssetTACm, Quote: exchange.AssetUSDT}
			if trs, terr := n.exchangeSvc.RecentTrades(m, 1); terr == nil && len(trs) > 0 {
				if p, perr := strconv.ParseFloat(trs[0].PriceStr, 64); perr == nil {
					n.walletSvc.SetTACMPrice(p)
				} else {
					n.walletSvc.SetTACMPrice(0)
				}
			} else {
				n.walletSvc.SetTACMPrice(0)
			}
		}
		if _, cerr := n.walletSvc.EnsureTiUSDCap(); cerr != nil {
			fmt.Printf("[tacm] v102 priceCapLoop: %v\n", cerr)
		}
	}
}

// seedDefiLiquidity 為 DeFi 池與借貸市場注資初始流動性（Reward 鑄造、帳戶為空才執行，冪等）。
func (n *Node) seedDefiLiquidity() error {
	if n.defiSvc == nil || n.walletSvc == nil {
		return nil
	}
	pools, err := n.defiSvc.Pools()
	if err != nil {
		return err
	}
	for _, p := range pools {
		if err := n.seedAccountIfEmpty(defi.PoolAccount(p.ID), p.Token0, p.Reserve0, "defi:pool"+defi.PoolAccount(p.ID)+":seed"); err != nil {
			return err
		}
		if err := n.seedAccountIfEmpty(defi.PoolAccount(p.ID), p.Token1, p.Reserve1, "defi:pool"+defi.PoolAccount(p.ID)+":seed"); err != nil {
			return err
		}
	}
	markets, err := n.defiSvc.LendingMarkets()
	if err != nil {
		return err
	}
	for _, m := range markets {
		if err := n.seedAccountIfEmpty(defi.LendingAccount, m.Asset, m.TotalDeposit, "defi:lending:seed"); err != nil {
			return err
		}
	}
	vaults, err := n.defiSvc.Vaults()
	if err != nil {
		return err
	}
	for _, v := range vaults {
		if err := n.seedAccountIfEmpty(defi.VaultAccount(v.ID), v.UnderlyingAsset, v.TotalAssets, "defi:vault"+defi.VaultAccount(v.ID)+":seed"); err != nil {
			return err
		}
	}
	return nil
}

// seedAccountIfEmpty 帳戶為空時以 Reward 注資指定金額（冪等）。
func (n *Node) seedAccountIfEmpty(account, asset string, amount float64, memo string) error {
	acc, err := n.walletSvc.Balance(account)
	if err != nil {
		return fmt.Errorf("defi: 查詢注資帳戶 %s: %w", account, err)
	}
	// v90：先 normalize 為 wallet canonical Asset，避免 db 大小寫殘留導致精度誤判。
	canon, ok := wallet.NormalizeAsset(asset)
	if !ok {
		return fmt.Errorf("defi: 未知注資資產 %s", asset)
	}
	amts, err := wallet.NewAmount(amountStr(amount), canon)
	if err != nil {
		return fmt.Errorf("defi: 注資金額無效 %s/%s: %w", account, asset, err)
	}
	if accountBalanceIsZero(acc, wallet.Asset(asset)) {
		if wallet.Asset(asset) == wallet.AssetTiUSD {
			// TiUSD 由鏈上機制鑄造（供給層 MintTiUSD 增加供給並入帳），非私自發行。
			if _, err := n.walletSvc.MintTiUSD(account, amts.I64, memo+":chain-mint"); err != nil {
				return fmt.Errorf("defi: 鏈上鑄造注資 %s TiUSD: %w", account, err)
			}
			return nil
		}
		if err := n.walletSvc.Reward(account, wallet.Asset(asset), amts, memo); err != nil {
			return fmt.Errorf("defi: 注資 %s %s: %w", account, asset, err)
		}
	}
	return nil
}

func amountStr(f float64) string {
	return fmt.Sprintf("%.6f", f)
}

// accountBalanceIsZero 判斷帳戶某資產餘額是否為零（TACm 為 big.Int、穩定幣為 int64）。
func accountBalanceIsZero(a *wallet.Account, asset wallet.Asset) bool {
	switch asset {
	case wallet.AssetTACm:
		return a.TACmBalance == nil || a.TACmBalance.Sign() == 0
	case wallet.AssetTiUSD:
		return a.TiUSDBalance == 0
	default:
		return a.USDTBalance == 0
	}
}

func (n *Node) loadOrCreateKey(dataDir string) error {
	keyPath := filepath.Join(dataDir, "node_key.json")
	if raw, err := os.ReadFile(keyPath); err == nil {
		var kd struct {
			PrivateKey string `json:"private_key"`
			Address    string `json:"address"`
		}
		if err := json.Unmarshal(raw, &kd); err != nil {
			return fmt.Errorf("node: 解析節點密鑰失敗: %w", err)
		}
		pk, err := hex.DecodeString(kd.PrivateKey)
		if err != nil {
			return fmt.Errorf("node: 節點私鑰非法: %w", err)
		}
		kp, err := crypto.KeyPairFromPrivateKey(pk)
		if err != nil {
			return err
		}
		n.keypair = kp
	} else if errors.Is(err, os.ErrNotExist) {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			return err
		}
		addr, _ := kp.Address()
		kd := map[string]string{"private_key": hex.EncodeToString(kp.PrivateKey()), "address": addr}
		raw, _ := json.MarshalIndent(kd, "", "  ")
		if err := os.WriteFile(keyPath, raw, 0o600); err != nil {
			return fmt.Errorf("node: 寫入節點密鑰失敗: %w", err)
		}
		n.keypair = kp
	} else {
		return fmt.Errorf("node: 讀取節點密鑰失敗: %w", err)
	}
	addr, err := n.keypair.Address()
	if err != nil {
		return err
	}
	n.nodeAddress = addr
	return nil
}

// blockLookup 適配動態難度包的 BlockLookup 回調。
func (n *Node) blockLookup(height int64) (float64, bool) {
	b, err := n.db.GetBlock(height)
	if err != nil || b == nil {
		return 0, false
	}
	return float64(b.Ts), true
}

// Address 返回節點的鏈上地址。
// Auth 回傳會員認證服務（供 Web 層讀取登入會員資訊）。
func (n *Node) Auth() *AuthService { return n.authSvc }

// Governance 回傳鏈上治理管理器。
func (n *Node) Governance() *governance.Governance { return n.gov }

// Multisig 回傳鏈上多簽錢包管理器（v129；Gnosis Safe 模式）。
func (n *Node) Multisig() *MultisigManager { return n.ms }

// Auction 回傳鏈上拍賣管理器（v130）。
func (n *Node) Auction() *AuctionManager { return n.auc }

// applyGovernanceParam 治理參數執行回調：熱更新可運行時套用的參數。
// block_time/difficulty 立即生效；其餘參數儲存供 API 查詢與重啟後讀取。
func (n *Node) applyGovernanceParam(key, value string) error {
	if value == "" {
		return errors.New("node: 治理參數值為空")
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("node: 治理參數值非數字: %w", err)
	}
	switch key {
	case "block_time":
		if v < 0.5 || v > 60 {
			return errors.New("node: block_time 超出允許範圍")
		}
		n.mu.Lock()
		n.blockTime = time.Duration(v * float64(time.Second))
		n.mu.Unlock()
		return nil
	case "difficulty":
		if v < 1 || v > 100 {
			return errors.New("node: difficulty 超出允許範圍")
		}
		n.mu.Lock()
		n.baseDiff = int(v)
		n.adjDiff = int(v)
		n.mu.Unlock()
		return nil
	case "tx_fee_bps", "tiusd_floor_bps":
		// 已通過參數：儲存於治理庫，API 可查詢；重啟後由節點讀取套用。
		return nil
	case "gov_quorum_pct", "gov_timelock_blocks", "gov_proposer_min", "gov_total_supply", "treasury_fee_bps":
		// M151a：治理參數熱更新（透過鏈上提案修改，去中心化治理）。
		if v < 0 {
			return fmt.Errorf("node: %s 不能為負", key)
		}
		g := n.gov
		if g == nil {
			return errors.New("node: governance disabled")
		}
		cur := g.Config()
		switch key {
		case "gov_quorum_pct":
			if v > 100 {
				return errors.New("node: gov_quorum_pct 超出允許範圍")
			}
			cur.QuorumPct = v
		case "gov_timelock_blocks":
			if v > 1000000 {
				return errors.New("node: gov_timelock_blocks 超出允許範圍")
			}
			cur.TimelockBlocks = int64(v)
		case "gov_proposer_min":
			if v > 100 {
				return errors.New("node: gov_proposer_min 超出允許範圍")
			}
			cur.ProposerMin = v
		case "gov_total_supply":
			cur.TotalSupply = v
		case "treasury_fee_bps":
			if v > 10000 {
				return errors.New("node: treasury_fee_bps 超出允許範圍")
			}
			cur.TreasuryFeeBps = int64(v)
		}
		if key == "treasury_fee_bps" && n.walletSvc != nil {
			n.walletSvc.SetTreasuryFeeBps(int(v))
		}
		return g.SetConfig(cur)
	default:
		return fmt.Errorf("node: 未知治理參數 %q", key)
	}
}

func (n *Node) Address() string { return n.nodeAddress }

// IdentityPubHex 返回節點壓縮公鑰十六進制（用於創世驗證人規格）。
func (n *Node) IdentityPubHex() string {
	return hex.EncodeToString(n.keypair.PublicKeyCompressed())
}

// DB 暴露底層存儲（供 RPC 層查詢）。
func (n *Node) DB() *chaindb.ChainDB { return n.db }

// DataDir 回傳節點資料目錄（M148：節點面板儲存 node_config.json）。
func (n *Node) DataDir() string { return n.cfg.DataDir }

// Start 啟動出塊循環。
func (n *Node) Start() {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.mu.Unlock()
	n.wg.Add(3)
	go n.consensusLoop()
	go n.runBFT()
	go n.minerHeartbeatLoop()
	n.StartDynamicValidators() // M87：動態驗證人集週期重建（內部自行 wg.Add(1)）
	go n.healWalletSync()      // M103：錢包帳本/chain_stats 自癒（落後補同步＋產出計數修正）
	// M152：鏈上預言機——背景 goroutine 每 60s 刷新喂價快照。
	n.oracleSvc = oracle.New()
	n.oracleSvc.Start(context.Background(), 60*time.Second)
	// M154：帳戶抽象（EIP-4337）——以節點金鑰位址作為 Bundler 代執行者。
	if addr, aerr := n.keypair.Address(); aerr == nil {
		n.aaSvc = aa.New(addr)
	}
}

// minerHeartbeatLoop M60：伺服器端礦機排程——每 30s 對所有已「開機」的礦工續發
// 鏈上 hb:on 心跳交易（取代 M33 的純本地 TickMiner）：離開頁面仍持續挖礦，
// 且「在線狀態」寫上鏈，全網任何節點看到同一組在線礦工。
// 「開機/關機」由 /api/miner/start|stop 控制（開機即發 hb:on、關機即發 hb:off）。
func (n *Node) minerHeartbeatLoop() {
	defer n.wg.Done()
	interval := n.heartbeatInterval
	if interval <= 0 {
		interval = hbRefresh
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-t.C:
			n.submitActiveHeartbeats()
		}
	}
}

// M149m-8：本節點是否已鏈上質押成為驗證人（PoS 出塊資格）。
// 去中心化判定：質押中的節點應以分布式 BFT 啟動參與輪值出塊，而非純跟隨。
func (n *Node) IsValidator() bool {
	vals, err := n.db.GetPosValidators()
	if err != nil {
		return false
	}
	for _, v := range vals {
		if v.Address == n.nodeAddress && v.Status == "active" {
			return true
		}
	}
	return false
}

// StartAsFollower 以跟隨/全節點模式啟動：不主動 PoT 出塊，
// 僅參與 BFT 投票，並通過 P2P gossip/同步接收區塊。
//
// M61：follower 同樣啟動 minerHeartbeatLoop——Render 等入口以 -p2p-follower 運行，
// 使用者在本節點開機的礦工由該循環每 heartbeatInterval 續發 hb:on；否則首筆 hb:on
// 在 hbWindow 塊窗口後過期、礦工無聲下線（即使前端仍顯示挖礦中）。
func (n *Node) StartAsFollower() {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.mu.Unlock()
	n.wg.Add(2)
	go n.runBFT()
	go n.minerHeartbeatLoop()
	go n.healWalletSync()
	n.StartDynamicValidators() // M87：follower 同樣週期重建動態驗證人集
}

// StartDistributed 以分佈式 BFT 模式啟動：每個高度僅輪值 proposer
// PoT 出塊並廣播，其餘驗證人接收區塊並投票；最終性由跨節點聚合
// >2/3 precommit 達成；提議超時自動 view change（round+1）。
//
// M61：錨點同樣啟動 minerHeartbeatLoop（跟進 M60）——錨點本機 active=1 的礦工
// （例如營運方經 API 開機者）由循環持續續命上鏈；無 active 礦工時 submitActiveHeartbeats
// 為空操作，不產生交易、不影響 nonce。節點自身預設礦機 active=0，依 M36 設計不參與分潤。
func (n *Node) StartDistributed() {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.distributed = true
	n.consensusWake = make(chan struct{}, 8)
	n.mu.Unlock()
	n.wg.Add(3)
	go n.consensusLoop()
	go n.runBFT()
	go n.minerHeartbeatLoop()
	// v56：分布式驗證人與單節點同等服務——動態驗證人集週期重建
	// （鏈上新增質押的驗證人最遲 60s 納入輪值，對應主流鏈 epoch 式驗證人輪換）、
	// 帳本自癒、鏈上預言機、帳戶抽象 Bundler。
	n.StartDynamicValidators() // 內部自行 wg.Add(1)
	go n.healWalletSync()      // 帳本/chain_stats 自癒（落後補同步＋產出計數修正）
	n.oracleSvc = oracle.New()
	n.oracleSvc.Start(context.Background(), 60*time.Second)
	if addr, aerr := n.keypair.Address(); aerr == nil {
		n.aaSvc = aa.New(addr)
	}
}

// SuspendConsensus 暫停出塊/共識循環（保留 DB 與鏈狀態），供節點下線/維護，
// 亦用於測試模擬主鏈整機掛機。重啟時以 ResumeConsensus 恢復；恢復後若本機
// tip 落後於網路，catch-up sync 會以較長鏈規則自動追平。
func (n *Node) SuspendConsensus() {
	n.mu.Lock()
	if !n.running {
		n.mu.Unlock()
		return
	}
	n.running = false
	close(n.stopCh)
	n.stopCh = make(chan struct{})
	n.mu.Unlock()
	n.wg.Wait() // 等待出塊/BFT/心跳循環退出
}

// ResumeConsensus 恢復被 SuspendConsensus 暫停的出塊/共識循環。
func (n *Node) ResumeConsensus() {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	// v54 修復：直接讀 distributed 欄位（已持 n.mu），勿呼叫
	// isDistributed()——後者會再次 Lock 造成非重入死鎖。
	if n.distributed {
		n.wg.Add(3)
		go n.consensusLoop()
		go n.runBFT()
		go n.minerHeartbeatLoop()
	} else {
		n.wg.Add(2)
		go n.runBFT()
		go n.minerHeartbeatLoop()
	}
	n.mu.Unlock()
}

// closeAllStores 關閉所有由 New()/Start() 開啟的子服務及其底層資料庫（各自 .db/-wal/-shm）。
// 每個 store 都帶 nil 判斷、忽略 close error（單一 store 已關閉不應中斷其餘關閉），
// 供 running 與非 running 兩條 Close() 路徑共用，避免漏關任一 store 造成 FD 長期累積
// （主網長跑 OOM/斷線與單一測試程序檔案描述元耗盡的根因）。
func (n *Node) closeAllStores() {
	// 合約層與鏈上 DEX（各自持有獨立 .db）。
	if n.contracts != nil {
		n.contracts.Close()
	}
	if n.dex != nil {
		_ = n.dex.Close()
	}
	// L2 與跨鏈橋（僅配置時存在）。
	if n.l2svc != nil {
		n.l2svc.Close()
	}
	if n.l2 != nil {
		_ = n.l2.Close()
	}
	if n.bridge != nil {
		_ = n.bridge.Close()
	}
	n.stopAllRelays()
	// 錢包／交易所（需 Store() 取得底層 bolt 控制代碼）。
	if n.walletSvc != nil {
		_ = n.walletSvc.Store().Close()
	}
	if n.exchangeSvc != nil {
		_ = n.exchangeSvc.Store().Close()
	}
	// 其餘 New() 階段即開啟的 bolt/sqlite 子服務。
	if n.communitySvc != nil {
		_ = n.communitySvc.Close()
	}
	if n.defiSvc != nil {
		_ = n.defiSvc.Close()
	}
	if n.ensSvc != nil {
		_ = n.ensSvc.Close()
	}
	if n.c2cSvc != nil {
		_ = n.c2cSvc.Close()
	}
	if n.dexv3St != nil {
		_ = n.dexv3St.Close()
	}
	if n.perpSt != nil {
		_ = n.perpSt.Close()
	}
	if n.aimSt != nil {
		_ = n.aimSt.Close()
	}
	if n.aivSt != nil {
		_ = n.aivSt.Close()
	}
	if n.obSt != nil {
		_ = n.obSt.Close()
	}
	if n.authSvc != nil {
		_ = n.authSvc.Close()
	}
	if n.gov != nil {
		_ = n.gov.Close()
	}
	if n.ls != nil {
		_ = n.ls.Close()
	}
	if n.ms != nil {
		_ = n.ms.Close()
	}
	if n.auc != nil {
		_ = n.auc.Close()
	}
	if n.posStore != nil {
		_ = n.posStore.Close()
	}
}

// Close 停止出塊循環並關閉所有子服務與底層資料庫。
func (n *Node) Close() error {
	// 先停狀態治理排程，避免關庫過程中仍在清理歷史資料。
	if n.pruner != nil {
		_ = n.pruner.Close()
	}
	n.mu.Lock()
	running := n.running
	if running {
		n.running = false
	}
	n.mu.Unlock()
	if running {
		close(n.stopCh)
		n.wg.Wait()
	}
	n.closeAllStores()
	return n.db.Close()
}

// waitFor 可中斷地等待 d；收到停止信號立即返回 false。
func (n *Node) waitFor(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-n.stopCh:
		return false
	case <-t.C:
		return true
	}
}

// consensusLoop 是出塊主循環：動態間隔 → 取池 → PoT → 驗證 → 寫塊 → 調難度。
func (n *Node) consensusLoop() {
	defer n.wg.Done()
	cs := &consensusState{lastRound: -1}
	for {
		select {
		case <-n.stopCh:
			return
		default:
		}

		// M32：節點自身默認礦機每輪心跳——保證鏈上任何時候都有在線算力歸屬（不再永遠「待機」）。
		if n.walletSvc != nil {
			_ = n.walletSvc.Store().TickMiner(n.nodeAddress)
		}

		// 動態出塊間隔：有交易快速（1s），無交易正常間隔。
		wait := n.blockTime
		if n.db.MempoolSize() > 0 {
			wait = fastBlockTime
		}
		if !n.waitConsensus(wait) {
			return
		}

		// 分佈式 BFT：提議超時 → view change；新輪次到來時立即重試出塊。
		n.advanceConsensusRound(cs)

		// v54：接管期間若網路實質落後（peer 領先 ≥2 塊）先追平再出塊，
		// 避免恢復/接管期同高度雙塊競賽（參照比特幣：未同步完成的節點不參與挖礦）。
		// 正常輪值（無接管標記）不攔截——短暫落後屬廣播延遲，proposer 照常出塊，
		// 否則 4 節點 BFT 的輪值節奏會被破壞（M12 回歸）。
		if n.TakeoverActive() && n.isBehindNetwork() {
			continue // 等 sync tick 追平（p2p sync 較長鏈優先）
		}

		if err := n.produceBlock(); err != nil {
			if errors.Is(err, errNotProposer) {
				// v54：失聯驗證人接管——輪值 proposer 提議超時（主鏈/創世節點
				// 掛機）且 view-change 無法聚合多數時，在線驗證人接管出塊，
				// 鏈不中斷；主鏈恢復後以較長鏈優先規則合併（M86-B）。
				if n.takeOverIfProposerOffline(cs) {
					continue // 已接管（本輪已嘗試出塊或接管標記生效）
				}
				continue // 非本輪提議者，回到頂部等待網絡塊/view-change
			}
			// 單輪失敗不致命，下一輪重試。
			fmt.Fprintf(os.Stderr, "[consensus] produce error: %v\n", err)
			n.waitFor(time.Second)
		}
	}
}

// isBehindNetwork 回報本機 tip 是否落後於在線 peer 的最高高度。
// v54：恢復/追同步期間僅由 catch-up sync 拉較長鏈，本節點暫不出塊，
// 追上後才恢復輪值出塊（參照比特幣：未同步完成的節點不參與挖礦）。
func (n *Node) isBehindNetwork() bool {
	if n.p2pNet == nil {
		return false
	}
	tip := n.db.GetTipHeight()
	for _, p := range n.p2pNet.PeerSnapshot() {
		// 領先 ≥2 塊才算實質落後：1 塊差距屬正常傳播延遲（proposer 剛出塊、
		// 其他節點尚未收到/應用），不攔截本節點輪值出塊。
		if p.Height > tip+1 {
			return true
		}
	}
	return false
}

// setTakeover 標記本節點接管指定高度出塊（失聯 proposer 補位）。
func (n *Node) setTakeover(height int64) {
	n.takeoverMu.Lock()
	defer n.takeoverMu.Unlock()
	n.takeover[height] = true
	// 只保留近期接管標記，避免無限增長。
	for h := range n.takeover {
		if h < height-1000 {
			delete(n.takeover, h)
		}
	}
}

// TakeoverStats 回傳本節點近期接管出塊統計（失聯 proposer 補位高度）。
// BlockProductionRate 本節點出塊率——掃最近 window 個區塊，統計 proposer=本節點 的比例（v127）。
func (n *Node) BlockProductionRate(window int) (mined, expected int64, rate float64, err error) {
	if window <= 0 {
		window = 100
	}
	blocks, err := n.db.GetBlocks(window, 0)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("node: 統計出塊率: %w", err)
	}
	addr := n.Address()
	for _, b := range blocks {
		if b.ProposerAddress != "" && b.ProposerAddress == addr {
			mined++
		}
	}
	expected = int64(len(blocks))
	if expected > 0 {
		rate = float64(mined) / float64(expected)
	}
	return mined, expected, rate, nil
}

func (n *Node) TakeoverStats() map[string]any {
	n.takeoverMu.Lock()
	defer n.takeoverMu.Unlock()
	heights := make([]int64, 0, len(n.takeover))
	for h := range n.takeover {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })
	count := len(heights)
	var latest int64
	if count > 0 {
		latest = heights[0]
	}
	if len(heights) > 20 {
		heights = heights[:20]
	}
	return map[string]any{
		"takeover_count":         count,
		"latest_takeover_height": latest,
		"takeover_heights":       heights,
	}
}

// isTakeover 查詢本節點是否已接管指定高度。
func (n *Node) isTakeover(height int64) bool {
	n.takeoverMu.Lock()
	defer n.takeoverMu.Unlock()
	return n.takeover[height]
}

// takeOverIfProposerOffline 判定並接管失聯 proposer 的高度：
//   - 僅分佈式 BFT 模式（多驗證人網路）且本節點在共識驗證人集內
//     （vset 視圖成員＝有出塊資格；對應鏈上質押驗證人/創世驗證人）；
//   - 目標高度自進入起已超過 consensusTimeout（proposer 未出塊、view-change 無進展）；
//   - 或本節點已處接管狀態（takeoverActive）——後續高度免等待持續出塊。
//
// 條件成立則標記接管並返回 true——consensusLoop 下輪 produceBlock 將繞過
// proposer 檢查直接出塊（該高度原 proposer 失聯的補位，等同比特幣礦工
// 離線後其餘節點繼續產出）。主鏈恢復（收到網絡塊推進 tip）後，新高度會
// 重置 offlineSince 並由收塊路徑清除 takeoverActive，自動回到正常輪值。
func (n *Node) takeOverIfProposerOffline(cs *consensusState) bool {
	if !n.isDistributed() {
		return false
	}
	if n.vset == nil || n.vset.Get(n.nodeAddress) == nil {
		return false // 本節點不在共識驗證人集內，無接管出塊資格
	}
	tip := n.db.GetTipHeight()
	target := tip + 1

	// 原 proposer/主鏈已恢復（peer 在線重連且其高度已達接管目標）→ 結束接管，
	// 回到正常輪值；恢復節點靠接管期缺口觸發的 catch-up 追平較長鏈
	// （isBehindNetwork 暫不出塊），追平後由輪值 proposer 正常出塊，
	// 避免同高度雙塊分叉競賽。
	if n.p2pNet != nil {
		for _, p := range n.p2pNet.PeerSnapshot() {
			if p.Online && p.Height >= target {
				n.takeoverMu.Lock()
				if n.takeoverActive {
					n.takeoverActive = false
				}
				n.takeoverMu.Unlock()
				return false
			}
		}
	}
	_, round, _ := n.bftNode.RoundInfo()

	n.takeoverMu.Lock()
	if n.takeoverActive {
		n.takeover[target] = true
		n.takeoverMu.Unlock()
		return true
	}
	n.takeoverMu.Unlock()

	// 提議尚未超時 → 不接管（給原 proposer 正常時間窗）；正常輪值未超時
	// 不觸發任何對端刷新，避免每輪 HTTP hello 干擾 BFT 節奏。
	if time.Since(cs.offlineSince) <= n.consensusTimeout(round) {
		return false
	}
	// v54：接管僅在「本節點是唯一在線驗證人」時觸發（主鏈/創世節點掛機、
	// 無其他驗證人可輪值接棒的少數場景）；有其他在線驗證人時交給 view change
	// 正常輪值接棒，避免接管與 view-change 同時出塊造成分叉（4 節點 BFT 回歸）。
	if n.vset != nil && n.p2pNet != nil {
		for _, p := range n.p2pNet.PeerSnapshot() {
			if p.Online && n.vset.Get(p.Owner) != nil {
				return false
			}
		}
	}
	// 超時（proposer 可能失聯）：主動刷新對端高度再判定一次——peer 高度快照
	// 可能滯後（心跳 15s），正常在線 proposer（僅廣播/心跳滯後）不得被誤判
	// 失聯而接管雙出（曾導致 4 節點 BFT 於 view-change 後分叉）。
	if n.p2pNet != nil {
		n.p2pNet.RefreshHeights()
		for _, p := range n.p2pNet.PeerSnapshot() {
			if p.Online && p.Height >= target {
				n.takeoverMu.Lock()
				if n.takeoverActive {
					n.takeoverActive = false
				}
				n.takeoverMu.Unlock()
				return false
			}
		}
	}
	n.takeoverMu.Lock()
	n.takeoverActive = true
	n.takeover[target] = true
	n.takeoverMu.Unlock()
	return true
}

// consensusState 為 consensusLoop 跨輪保留的輪次計時狀態。
type consensusState struct {
	target    int64
	since     time.Time
	lastRound int32
	// v54：進入該高度的時刻（獨立於 view-change 計時；用於失聯 proposer 接管判定）。
	offlineSince time.Time
}

// waitConsensus 等待下一個出塊時機；分佈式模式下可被 view-change 喚醒。
func (n *Node) waitConsensus(wait time.Duration) bool {
	if !n.isDistributed() {
		return n.waitFor(wait)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-n.stopCh:
		return false
	case <-n.consensusWake:
		return true
	case <-t.C:
		return true
	}
}

// wakeConsensus 非阻塞喚醒 consensusLoop。
func (n *Node) wakeConsensus() {
	if n.consensusWake == nil {
		return
	}
	select {
	case n.consensusWake <- struct{}{}:
	default:
	}
}

func (n *Node) isDistributed() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.distributed
}

// consensusTimeout 返回某輪次的提議超時：base×2^round，上限 60s。
func (n *Node) consensusTimeout(round int32) time.Duration {
	base := 3 * n.blockTime
	if base < 2*time.Second {
		base = 2 * time.Second
	}
	if round <= 0 {
		return base
	}
	if round > 30 {
		return 60 * time.Second
	}
	d := base * (1 << round)
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}

// advanceConsensusRound 推進共識輪次：確保 BFT 進入目標高度；若該高度
// 提議超時則 round+1 並廣播 view-change。狀態存於 cs 供下輪使用。
func (n *Node) advanceConsensusRound(cs *consensusState) {
	if !n.isDistributed() {
		return
	}
	tip := n.db.GetTipHeight()
	target := tip + 1
	if target != cs.target {
		// 進入新高度：重置計時。
		cs.target = target
		cs.since = time.Now()
		cs.offlineSince = time.Now() // v54：新高度開始計失聯時長
		cs.lastRound = -1
	}
	if target <= 0 {
		return
	}

	// 若 BFT 尚未啟動該高度（提案未到），先 StartHeight 以便輪次推進。
	if h, _, _ := n.bftNode.RoundInfo(); h != target {
		n.bftNode.StartHeight(target)
	}
	_, round, _ := n.bftNode.RoundInfo()
	if round != cs.lastRound {
		cs.since = time.Now()
		cs.lastRound = round
	}

	// 提案已到達（tip ≥ target）：下輪自然進入新高度。
	if tip >= target {
		return
	}

	// 超時：M12 硬化——本節點發 view-change 票（簽名廣播），等待 >2/3 多數認證後切輪。
	// 非分佈式單節點直接切輪（無需網絡認證）。
	if time.Since(cs.since) > n.consensusTimeout(round) {
		if !n.isDistributed() {
			n.bftNode.MoveToNextRound()
			_, newRound, _ := n.bftNode.RoundInfo()
			cs.since = time.Now()
			cs.lastRound = newRound
			return
		}
		n.proposeViewChange(target)
		// 若多數已在（例如其他節點的票已到），recordViewChangeTicket 已切輪；
		// 否則等待網絡聚合。重設計時避免每 tick 重複發票。
		cs.since = time.Now()
	}
}

func (n *Node) produceBlock() error {
	latest, err := n.db.GetLatestBlock()
	if err != nil {
		return err
	}
	var height int64 = 1
	var prevPtr *string
	var prevForHeader any // Canonical 僅接受 nil/string，不接受指針
	if latest != nil {
		height = latest.Height + 1
		h := latest.Hash
		prevPtr = &h
		prevForHeader = h
	}

	// 分佈式 BFT：僅輪值 proposer 出塊，其餘節點等待網絡傳來的提議。
	// 使用當前 BFT round（view change 後由下一個 proposer 接棒）。
	// v54：接管高度（原 proposer 失聯補位）由在線驗證人出塊。
	if n.isDistributed() {
		_, round, _ := n.bftNode.RoundInfo()
		proposer := n.vset.Proposer(height, round)
		if proposer == nil || (proposer.Address != n.nodeAddress && !n.isTakeover(height)) {
			return errNotProposer
		}
	}

	// M74-3 EIP-1559：本塊 base fee 由父塊的 base fee 與 gas 使用率調整。
	// MEV：baseFee 計算提前到 mempool 取件之前——打包排序需用本塊 baseFee
	// 計算每筆交易的有效優先小費（tipPerGas × gas）。
	parentBaseFee := chaindb.InitialBaseFee
	parentGasUsed := int64(0)
	parentGasLimit := int64(chaindb.BlockGasLimit)
	if latest != nil {
		if pf, perr := strconv.ParseFloat(latest.BaseFee, 64); perr == nil && pf > 0 {
			parentBaseFee = pf
		}
		parentGasUsed = latest.GasUsed
		parentGasLimit = latest.GasLimit
		if parentGasLimit <= 0 {
			parentGasLimit = chaindb.BlockGasLimit
		}
	}
	baseFee := chaindb.ComputeNextBaseFee(parentBaseFee, parentGasUsed, parentGasLimit)

	// M85/MEV：可打包佇列——每地址按 nonce 依序、同 nonce 取高 fee、nonce gap 截斷；
	// 地址間按「隊首有效優先小費」降序輪流（策略由節點 config 配置，預設 PriorityTip）。
	mempool, err := n.db.GetMempoolPendingSorted(maxTxPerBlock, baseFee, chaindb.ParsePackStrategy(n.cfg.PackStrategy))
	if err != nil {
		return err
	}

	ts := time.Now().Unix()
	txHashes := make([]string, 0, len(mempool)+1)
	txs := make([]chaindb.Transaction, 0, len(mempool)+1)
	gasUsed := int64(0)
	burnedTotal := 0.0

	// coinbase 增發交易置於首位（height>0），其哈希進入 Merkle 根。
	// 供應模型：TACM_MAX_SUPPLY 設定後依年衰減發行（上限 52,003,300）；未設定維持減半模式。
	// 硬煞車（方案 B）：每塊 reward = min(模型獎勵, 剩餘可挖)；剩餘 ≤ 0 → 0，
	// 總供應永不高於上限，與出塊速度無關（reorg.go 驗證側共用 ComputeCappedReward）。
	// M58：coinbase 瓜分明細（礦工按算力 + 獎勵池 15%）寫入區塊 → 全網節點照單執行，帳本一致。
	if height > 0 {
		var minedWei *big.Int
		if n.walletSvc != nil {
			if mw, merr := n.walletSvc.ChainMinedTacm(); merr == nil {
				minedWei = mw
			}
		}
		reward := chaindb.ComputeCappedReward(height, int64(n.cfg.BlockTime), minedWei)
		var splits []chaindb.CoinbaseSplit
		var onlineNodes []string // M149m-7：鏈上在線節點集（含 0 算力），瓜分 45% 節點獎勵
		if n.walletSvc != nil {
			// M60：瓜分明細以「鏈上 heartbeat 視角」為準（取代各節點本地 SQL OnlineMinerSplits）：
			// 任何節點開機的礦工 hb:on 上鏈後，全網出塊者都把它算入瓜分；hb:off/過期即停。
			// 結果只依賴已同步區塊 → 錨點/節點瓜分完全一致，不再有入口差異。
			if states, err := n.onChainOnlineMiners(hbWindow); err == nil {
				for addr, hr := range states {
					if hr > 0 {
						splits = append(splits, chaindb.CoinbaseSplit{Address: addr, Share: hr})
					}
					onlineNodes = append(onlineNodes, addr) // 含 0 算力節點：在線即有 PoT 節點獎勵
				}
			}
		}
		// M157：委託獎勵分潤——節點 45% 獎勵中，委託占比（委託額/總權重）部分
		// 按委託人拆分發 coinbase:deleg；計算僅依賴 delegations＋pos_validators 表
		// （全網同步同鏈一致）→ 任何節點構建區塊得到相同拆分，恆等式不破。
		delegRewards := make(map[string][]chaindb.DelegationReward)
		if len(onlineNodes) > 0 {
			nodeShare := reward * float64(chaindb.NodeShareBps) / 10000
			per := nodeShare / float64(len(onlineNodes))
			if per > 1e-9 {
				vs, verr := n.db.GetPosValidators()
				if verr == nil {
					stakeByAddr := make(map[string]float64, len(vs))
					for _, v := range vs {
						stakeByAddr[v.Address] = v.TotalStake
					}
					for _, addr := range onlineNodes {
						dels, derr := n.db.GetValidatorDelegations(addr)
						if derr != nil || len(dels) == 0 {
							continue
						}
						totalDeleg := 0.0
						for _, d := range dels {
							totalDeleg += d.Amount
						}
						totalStake := stakeByAddr[addr]
						if totalDeleg <= 0 || totalStake <= 0 || totalDeleg >= totalStake-1e-9 {
							continue // 無委託或權重異常：不拆分
						}
						var list []chaindb.DelegationReward
						acc := 0.0
						for i, d := range dels {
							var amt float64
							if i == len(dels)-1 {
								amt = per*totalDeleg/totalStake - acc
							} else {
								amt = per * d.Amount / totalStake
								acc += amt
							}
							if amt > 1e-9 {
								list = append(list, chaindb.DelegationReward{Delegator: d.Delegator, Amount: amt})
							}
						}
						if len(list) > 0 {
							delegRewards[addr] = list
						}
					}
					// v75：流動質押池獎勵注入（主鏈本地服務）——池視為本節點委託人，
					// 按 池質押/(節點自質押+池質押) 分得本節點出塊獎勵份額；僅影響本地池匯率。
					if n.ls != nil {
						selfStake := stakeByAddr[n.nodeAddress]
						if share := n.ls.PoolShare(selfStake, nodeShare); share > 1e-9 {
							_ = n.ls.AddRewards(share)
						}
					}
				}
			}
		}
		for _, cb := range chaindb.BuildCoinbaseTxsWithDelegRewards(height, n.nodeAddress, ts, splits, reward, delegRewards, onlineNodes...) {
			txs = append(txs, cb)
			txHashes = append(txHashes, cb.TxHash)
		}

		// M159：委託解綁期滿自動返還——依 unbondings pending 且 complete_height≤height
		// 構造 pos:unbond_pay（from 空、無簽名，coinbase 同款）；全節點由同表算出
		// 相同交易 → 區塊一致，返還金額即質押池鎖倉資產（非增發）。
		if ubs, uerr := n.db.ListPendingUnbondings(height); uerr == nil {
			for _, u := range ubs {
				ubTx := chaindb.Transaction{
					TxHash:   chaindb.CoinbaseSplitTxHash(height, len(txs), u.Delegator, u.Amount, ts),
					FromAddr: "", ToAddr: u.Delegator,
					Amount: chaindb.FormatFloat(u.Amount), Fee: "0", Nonce: 0, Ts: ts,
					Memo:   chaindb.UnbondingMemo + ":" + u.Delegator + ":" + u.Validator,
					Status: "confirmed",
				}
				txs = append(txs, ubTx)
				txHashes = append(txHashes, ubTx.TxHash)
			}
		}

		// M151c：treasury 撥款——已到期且通過的 treasury 提案由出塊引擎轉帳。
		// M151c-v2（白皮書合規）：資金來源＝治理金庫（treasury_pool，手續費預留）優先，
		// 不足時以創世社群基金（community_pool）補足；獎勵池恢復純做市用途、不再撥款。
		// 餘額不足時保留 queued，下個區塊重試（不硬執行、不超額）；全節點照單執行帳本一致。
		if n.walletSvc != nil && n.gov != nil {
			if pendings, perr := n.gov.PendingTreasury(height); perr == nil {
				for _, te := range pendings {
					amt, aerr := strconv.ParseFloat(te.Amount, 64)
					if aerr != nil || amt <= 0 {
						continue
					}
					src := wallet.TreasuryAddr
					bal, berr := n.walletSvc.Balance(wallet.TreasuryAddr)
					if berr != nil || bal.TACmBalance == nil {
						continue
					}
					balF, _ := strconv.ParseFloat(bal.TACmBalance.String(), 64)
					balF /= 1e18 // raw → TACM（與提案金額同尺度比較）
					if balF+1e-9 < amt {
						poolAddr, perr2 := n.CommunityPoolAddress()
						if perr2 != nil {
							continue
						}
						src = poolAddr
						bal2, berr2 := n.walletSvc.Balance(poolAddr)
						if berr2 != nil || bal2.TACmBalance == nil {
							continue
						}
						balF, _ = strconv.ParseFloat(bal2.TACmBalance.String(), 64)
						balF /= 1e18 // raw → TACM
						if balF+1e-9 < amt {
							continue // 餘額不足：保留 queued 重試
						}
					}
					gt := chaindb.Transaction{
						TxHash:   chaindb.GovTreasuryTxHash(height, te.ProposalID, te.To, amt, ts),
						FromAddr: src,
						ToAddr:   te.To,
						Amount:   chaindb.FormatFloat(amt),
						Fee:      "0", Nonce: 0, Ts: ts,
						Memo:   fmt.Sprintf("gov:treasury:%d", te.ProposalID),
						Status: "confirmed",
					}
					txs = append(txs, gt)
					txHashes = append(txHashes, gt.TxHash)
					_ = n.gov.MarkTreasuryExecuted(te.ProposalID)
				}
			}
		}
	}

	for _, m := range mempool {
		if h := getString(m, "tx_hash"); h != "" {
			txHashes = append(txHashes, h)
		}
		t := txFromMap(m)
		// M74-3：EIP-1559 交易拆分（burn/tip）與 gas 帳本；legacy 交易 burn=0。
		if t.MaxFee != "" {
			_, err1 := strconv.ParseFloat(t.MaxFee, 64)
			_, err2 := strconv.ParseFloat(t.PriorityFee, 64)
			ff, err3 := strconv.ParseFloat(t.Fee, 64)
			if err1 == nil && err2 == nil && err3 == nil && ff > 0 {
				t.GasLimit = chaindb.TxGasLimit(t.GasLimit)
				burn, _ := chaindb.SplitFee(ff, baseFee, t.GasLimit)
				t.Burned = chaindb.FormatFloat(burn)
			}
		}
		if t.GasLimit <= 0 {
			t.GasLimit = chaindb.TxGasBase
		}
		gasUsed += t.GasLimit
		txs = append(txs, t)
		if t.Burned != "" {
			if b, berr := strconv.ParseFloat(t.Burned, 64); berr == nil {
				burnedTotal += b
			}
		}
	}
	if gasUsed > chaindb.BlockGasLimit {
		return errors.New("block gas limit exceeded")
	}
	mroot, err := crypto.MerkleRootStrings(txHashes)
	if err != nil {
		return err
	}
	header := map[string]any{
		"height":      height,
		"prev_hash":   prevForHeader,
		"merkle_root": mroot,
		"proposer":    n.nodeID,
		"ts":          ts,
		"tx_count":    len(txs),
	}

	// PoT：當前有效難度，cap 8；失敗逐級降級重試（最多3次）。
	n.mu.Lock()
	diff := n.adjDiff
	n.mu.Unlock()
	if diff > difficulty.MaxDifficulty {
		diff = difficulty.MaxDifficulty
	}
	maxNonce := 200000
	if diff > 2 {
		maxNonce = 500000
	}

	pow, err := crypto.MineBlock(header, diff, maxNonce)
	for retries := 0; pow == nil && retries < 3 && diff > 1; retries++ {
		diff--
		pow, err = crypto.MineBlock(header, diff, maxNonce)
	}
	if err != nil {
		return fmt.Errorf("挖礦失敗: %w", err)
	}
	if pow == nil {
		return nil // 本輪未找到 nonce，跳過
	}

	header["nonce"] = pow.Nonce
	header["difficulty"] = diff
	ok, err := crypto.VerifyPow(header, diff)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("PoT 驗證失敗")
	}

	block := &chaindb.Block{
		Height:          height,
		Hash:            pow.Hash,
		PrevHash:        prevPtr,
		MerkleRoot:      mroot,
		Proposer:        n.nodeID,
		ProposerAddress: n.nodeAddress,
		Ts:              ts,
		TxCount:         len(txs),
		Difficulty:      diff,
		Nonce:           int64(pow.Nonce),
		BaseFee:         chaindb.FormatFloat(baseFee),
		GasUsed:         gasUsed,
		GasLimit:        chaindb.BlockGasLimit,
		Burned:          chaindb.FormatFloat(burnedTotal),
	}
	if err := n.db.InsertBlock(block, txs); err != nil {
		return err
	}
	n.resetPendingTx()
	n.dexStakeTick(height)

	// 執行塊內合約交易（部署/調用），持久化 code/storage。
	if err := n.executeBlockContracts(txs); err != nil {
		return err
	}

	// 錢包帳本同步（coinbase 獎勵入帳 + 交易轉帳 + 手續費）。
	if err := n.syncWallet(height, txs); err != nil {
		return fmt.Errorf("錢包同步 h=%d: %w", height, err)
	}
	// 本節點接受新塊，啟動/推進 BFT 最終性投票。
	n.onNewBlock(height, block.Hash)

	// 若已聯網，向 P2P 網絡廣播新塊（含 coinbase 與打包交易）。
	if n.p2pNet != nil {
		n.p2pNet.BroadcastBlock(block, txs)
	}

	// 調整點：按剛完成窗口重算基礎難度。
	if difficulty.ShouldRetarget(height, difficulty.RetargetInterval) {
		win := difficulty.WindowTimestampsForHeight(
			n.blockLookup, height, difficulty.RetargetInterval)
		n.mu.Lock()
		newBase := difficulty.ComputeRetarget(n.adjDiff, win, n.blockTime.Seconds())
		n.adjDiff = newBase
		n.mu.Unlock()
	}

	// M40 鏈上治理：每個新區塊驅動到期提案統計與參數執行。
	// M151：時間鎖提案由 ExecuteDue 於延遲屆滿後執行。
	if n.gov != nil {
		n.gov.TallyAndExecute(height)
		n.gov.ExecuteDue(height)
	}
	return nil
}
