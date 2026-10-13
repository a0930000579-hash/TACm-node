package node

// M129-C：eth_getProof（EIP-1186）——帳戶與儲存槽證明。
//
// TACm 使用 key-value 狀態庫（非 Merkle Patricia Trie），因此 accountProof /
// storageProof 回空陣列（結構與以太標準相容）；balance / nonce / codeHash /
// storageHash / storageProof.value 均為鏈上真實資料。

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

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
