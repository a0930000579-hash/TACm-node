package node

// M129-C：eth_getProof（EIP-1186）——帳戶與儲存槽證明。
//
// TACm 使用 key-value 狀態庫（非 Merkle Patricia Trie），因此 accountProof /
// storageProof 回空陣列（結構與以太標準相容）；balance / nonce / codeHash /
// storageHash / storageProof.value 均為鏈上真實資料。

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// ethEmptyCodeHash 為以太標準空程式碼哈希（keccak256("")）。
var ethEmptyCodeHash = "0x" + hex.EncodeToString(crypto.Keccak256([]byte{}))

// ethGetProof 依 EIP-1186 回傳帳戶證明與指定儲存槽證明。
// params: [address, storageKeys[], blockNumber]
func (s *RPCServer) ethGetProof(params []any) (map[string]any, error) {
	addr, err := s.node.normalizeEthAddr(paramString(params, 0))
	if err != nil {
		return nil, err
	}
	// storage keys：接受 0x hex 或 十進位字串。
	var keys []string
	if arr, ok := params[1].([]any); ok {
		for _, k := range arr {
			ks, _ := k.(string)
			if ks == "" {
				continue
			}
			keys = append(keys, ks)
		}
	}
	addr0x, err := tx0To0x(addr)
	if err != nil {
		return nil, err
	}
	balance := s.node.db.GetBalance(addr)
	nonce := s.node.db.GetNonce(addr)
	code := s.node.contracts.GetCode(addr0x)

	codeHash := ethEmptyCodeHash
	if len(code) > 0 {
		codeHash = "0x" + hex.EncodeToString(crypto.Keccak256(code))
	}
	// 儲存根：TACm 為 key-value 庫、無 trie root——以 keccak("") 標記「無 trie 證明」。
	storageHash := ethEmptyCodeHash

	storageProof := make([]any, 0, len(keys))
	for _, k := range keys {
		keyBig, kerr := proofKeyBig(k)
		if kerr != nil {
			continue
		}
		val := s.node.contracts.StorageAt(addr0x, keyBig)
		storageProof = append(storageProof, map[string]any{
			"key":   "0x" + fmt.Sprintf("%064x", keyBig),
			"value": "0x" + storageValueHex(val),
			"proof": []string{},
		})
	}

	return map[string]any{
		"address":      addr0x,
		"accountProof": []string{},
		"balance":      "0x" + tacmToWeiHexBig(balance),
		"codeHash":     codeHash,
		"nonce":        "0x" + strconv.FormatInt(nonce, 16),
		"storageHash":  storageHash,
		"storageProof": storageProof,
	}, nil
}

// proofKeyBig 把 0x hex / 十進位儲存槽 key 轉 big.Int。
func proofKeyBig(k string) (*big.Int, error) {
	ks := k
	if len(ks) > 2 && ks[:2] == "0x" {
		ks = ks[2:]
	}
	b, err := hex.DecodeString(ks)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// storageValueHex 把 StorageAt 回傳的 any（*big.Int / string / uint64）轉 64-hex。
func storageValueHex(v any) string {
	switch t := v.(type) {
	case *big.Int:
		if t == nil {
			return "0"
		}
		return fmt.Sprintf("%064x", t)
	case string:
		bi, ok := new(big.Int).SetString(t, 10)
		if !ok {
			return "0"
		}
		return fmt.Sprintf("%064x", bi)
	case int64:
		return fmt.Sprintf("%064x", t)
	case uint64:
		return fmt.Sprintf("%064x", t)
	case int:
		return fmt.Sprintf("%064x", t)
	default:
		return "0"
	}
}

// tacmToWeiHexBig 把 TACm 餘額字串（十進位）轉 wei big.Int。
func tacmToWeiHexBig(bal string) string {
	rat, ok := new(big.Rat).SetString(bal)
	if !ok {
		return "0"
	}
	wei := new(big.Int).Quo(
		new(big.Int).Mul(rat.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
		rat.Denom())
	return wei.Text(16)
}

// debugStorageRangeAt 依 geth debug 面：枚舉指定帳號儲存槽（分頁）。
// params: [blockHash, txIndex, address, startKey(0x hex), maxStorage]
// 回 {storage: {key: value}, nextKey: 下一分頁鍵或 null}。
func (s *RPCServer) debugStorageRangeAt(params []any) (map[string]any, error) {
	blockRef := paramString(params, 0)
	txIdx := 0
	var b *chaindb.Block
	if strings.HasPrefix(blockRef, "0x") && len(blockRef) == 66 {
		bb, err := s.node.db.GetBlockByHash(strip0x(blockRef))
		if err != nil || bb == nil {
			return nil, errors.New("debug: 區塊不存在: " + blockRef)
		}
		b = bb
	} else {
		latest, err := s.node.db.GetLatestBlock()
		if err != nil || latest == nil {
			return nil, errors.New("debug: 無區塊")
		}
		if blockRef == "latest" || blockRef == "pending" || blockRef == "" {
			b = latest
		} else if h := parseEthBlock(blockRef, latest.Height); h >= 0 {
			bb, berr := s.node.db.GetBlock(h)
			if berr != nil || bb == nil {
				return nil, errors.New("debug: 區塊不存在: " + blockRef)
			}
			b = bb
		} else {
			return nil, errors.New("debug: 區塊參數無法解析: " + blockRef)
		}
	}
	// params[1] = tx index（數量），僅驗證範圍。
	if len(params) > 1 {
		if n, ok := params[1].(float64); ok {
			txIdx = int(n)
		} else if st, ok2 := params[1].(string); ok2 {
			txIdx, _ = strconv.Atoi(st)
		}
	}
	txs, err := s.node.db.GetTransactionsByBlock(b.Height)
	if err != nil {
		return nil, err
	}
	if txIdx < 0 || txIdx >= len(txs) {
		return nil, errors.New("debug: tx index 超出區塊範圍")
	}
	addr, err := s.node.normalizeEthAddr(paramString(params, 2))
	if err != nil {
		return nil, err
	}
	addr0x, err := tx0To0x(addr)
	if err != nil {
		return nil, err
	}
	startKey := ""
	if len(params) > 3 {
		startKey = paramString(params, 3)
	}
	maxStorage := 1024
	if len(params) > 4 {
		if n, ok := params[4].(float64); ok && n > 0 {
			maxStorage = int(n)
		}
	}
	if maxStorage > 10000 {
		maxStorage = 10000
	}
	entries, err := s.node.contracts.StorageEntries(addr0x)
	if err != nil {
		return nil, err
	}
	// 排序 keys（32 字節 hex 字典序＝數值序）。
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	startNorm := strings.TrimPrefix(startKey, "0x")
	begun := startNorm == ""
	storage := map[string]any{}
	var nextKey any
	collected := 0
	for _, k := range keys {
		if !begun {
			if k >= startNorm {
				begun = true
			} else {
				continue
			}
		}
		if collected < maxStorage {
			storage["0x"+k] = "0x" + storageValueHex(entries[k])
			collected++
			continue
		}
		nextKey = "0x" + k
		break
	}
	if collected < maxStorage {
		nextKey = nil
	}
	return map[string]any{"storage": storage, "nextKey": nextKey}, nil
}
