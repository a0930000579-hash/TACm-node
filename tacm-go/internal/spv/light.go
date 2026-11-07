package spv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/consensus/bft"
)

// LightClient 為 SPV 輕節點：只保存區塊頭，按需向全節點取證明並本地驗證。
type LightClient struct {
	nodeURL    string
	maxHeaders int
	headers    map[int64]map[string]any
	vset       *bft.ValidatorSet
	client     *http.Client
}

// NewLightClient 構造輕客戶端。
func NewLightClient(nodeURL string, maxHeaders int) *LightClient {
	if maxHeaders <= 0 {
		maxHeaders = 400
	}
	return &LightClient{
		nodeURL:    strings.TrimRight(nodeURL, "/"),
		maxHeaders: maxHeaders,
		headers:    map[int64]map[string]any{},
		client:     &http.Client{Timeout: 8 * time.Second},
	}
}

func (lc *LightClient) get(path string) (map[string]any, int, error) {
	res, err := lc.client.Get(lc.nodeURL + path)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, res.StatusCode, err
	}
	out := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out, res.StatusCode, nil
}

// LoadValidators 拉取驗證人集（最終性驗證前置）。
func (lc *LightClient) LoadValidators() (bool, error) {
	data, sc, err := lc.get("/validators")
	if err != nil || sc != http.StatusOK {
		return false, err
	}
	rawList, ok := data["validators"].([]any)
	if !ok {
		return false, nil
	}
	vals := make([]bft.Validator, 0, len(rawList))
	for _, it := range rawList {
		vm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		vals = append(vals, bft.Validator{
			Address:   hdrStr(vm, "address"),
			PubkeyHex: hdrStr(vm, "pubkey_hex"),
			Power:     int(hdrInt(vm, "power")),
		})
	}
	vs, err := bft.NewValidatorSet(vals)
	if err != nil {
		return false, err
	}
	lc.vset = vs
	return true, nil
}

// SyncResult 為頭同步結果。
type SyncResult struct {
	Synced  int    `json:"synced"`
	ChainOK bool   `json:"chain_ok"`
	Reason  string `json:"reason"`
	Tip     int64  `json:"tip"`
}

// SyncHeaders 拉取最新一組區塊頭並驗證鏈接。
func (lc *LightClient) SyncHeaders(count int) (*SyncResult, error) {
	if count <= 0 {
		count = lc.maxHeaders
	}
	data, sc, err := lc.get("/headers?count=" + strconv.Itoa(count))
	if err != nil || sc != http.StatusOK {
		return nil, err
	}
	hs := toMapSlice(data["headers"])
	ok, reason := VerifyHeaderChain(hs)
	if ok {
		for _, h := range hs {
			lc.headers[hdrInt(h, "height")] = h
		}
	}
	var tip int64
	if t, ok := data["tip"].(json.Number); ok {
		tip, _ = t.Int64()
	}
	return &SyncResult{Synced: len(hs), ChainOK: ok, Reason: reason, Tip: tip}, nil
}

// TxVerifyResult 為交易 SPV 驗證結果。
type TxVerifyResult struct {
	Included      bool   `json:"included"`
	Height        int64  `json:"height"`
	Confirmations int64  `json:"confirmations"`
	Finalized     bool   `json:"finalized"`
	FinalityPower int    `json:"finality_power"`
	Error         string `json:"error,omitempty"`
}

// VerifyTransaction 本地驗證交易包含性、確認數與最終性（不信任節點結論）。
func (lc *LightClient) VerifyTransaction(txHash string) (*TxVerifyResult, error) {
	p, sc, err := lc.get("/proof/" + txHash)
	if err != nil || sc != http.StatusOK {
		return nil, err
	}
	found, _ := p["found"].(bool)
	if !found {
		return &TxVerifyResult{Error: "no proof"}, nil
	}
	height := hdrInt(p, "height")
	index := int(hdrInt(p, "index"))
	header, _ := p["header"].(map[string]any)
	mroot := hdrStr(header, "merkle_root")
	hash := hdrStr(header, "hash")

	if !VerifyMerkleProof(txHash, index, toStringSlice(p["proof"]), mroot) {
		return &TxVerifyResult{Error: "merkle proof verification failed"}, nil
	}
	if local, ok := lc.headers[height]; ok &&
		hdrStr(local, "merkle_root") != mroot {
		return &TxVerifyResult{Error: "local header mismatch"}, nil
	}

	tipH := height
	if status, _, e := lc.get("/status"); e == nil {
		tipH = hdrInt(status, "block_height")
	}
	confs := tipH - height + 1
	if confs < 0 {
		confs = 0
	}
	final, fp := lc.checkFinalized(height, hash)
	return &TxVerifyResult{
		Included: true, Height: height, Confirmations: confs,
		Finalized: final, FinalityPower: fp,
	}, nil
}

func (lc *LightClient) checkFinalized(height int64, blockHash string) (bool, int) {
	if lc.vset == nil {
		return false, 0
	}
	fp, sc, err := lc.get("/finality-proof/" + strconv.FormatInt(height, 10))
	if err != nil || sc != http.StatusOK {
		return false, 0
	}
	bh := hdrStr(fp, "block_hash")
	if bh == "" {
		bh = blockHash
	}
	return CheckFinalityProof(height, bh, toMapSlice(fp["votes"]), lc.vset)
}

// FinalityResult 為最終性驗證結果。
type FinalityResult struct {
	Finalized bool   `json:"finalized"`
	Power     int    `json:"power"`
	Quorum    int    `json:"quorum"`
	BlockHash string `json:"block_hash"`
}

// VerifyFinality 驗證指定高度是否已 BFT 最終化。
func (lc *LightClient) VerifyFinality(height int64) (*FinalityResult, error) {
	if lc.vset == nil {
		return nil, fmt.Errorf("validators not loaded; call LoadValidators first")
	}
	fp, sc, err := lc.get("/finality-proof/" + strconv.FormatInt(height, 10))
	if err != nil || sc != http.StatusOK {
		return nil, err
	}
	bh := hdrStr(fp, "block_hash")
	ok, power := CheckFinalityProof(height, bh, toMapSlice(fp["votes"]), lc.vset)
	return &FinalityResult{
		Finalized: ok, Power: power,
		Quorum: lc.vset.QuorumPower(), BlockHash: bh,
	}, nil
}

func toMapSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []map[string]any{}
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, it := range arr {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
