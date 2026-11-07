package vm

import (
	"sort"
)

// 本檔實作「區塊內 EVM 並行執行」的靜態分析與排程核心（對標 Sei / Block-STM）：
//
//  1. 交易存取集合分析（static read/write set）：以「規範化地址」為粒度。
//     合約呼叫觸及 caller 與目標合約地址，並對 calldata 做常見 selector 解碼
//     （ERC20 transfer/transferFrom/approve、ERC721/1155 安全轉移、常見 mint 等），
//     把位址參數加入預測集合。
//  2. 分層排程（greedy level scheduling）：把交易切成有序 layer，同一 layer 內
//     任兩筆預測存取集合兩兩不相交；跨 layer 維持原序。
//  3. 樂觀並行執行＋事後驗證＋串行回退的判斷原則（真正執行在 parallel_apply.go）。
//
// 設計邊界（安全優先，與串行結果逐字一致為最高鐵律）：
//   - 目標合約位元組碼含 CALL/STATICCALL/CALLCODE/DELEGATECALL/CREATE/CREATE2 等
//     opcode、或 calldata 無法解碼出已知 selector → 一律不可並行（走串行 lane）。
//   - 目標合約地址本身是「共享執行上下文」：它會出現在每筆同目標呼叫的實際寫入
//     集合中，但不參與 layer 間的衝突判定（外部地址＝caller＋解碼出的位址參數才是
//     衝突單元）。執行後的 slot 級合併與同 slot 寫入衝擊檢測在 parallel_apply.go 把關。

// forbiddenParallelOps 為「無法靜態保證隔離」的 opcode 集合：含此類 opcode 的合約
// 可能跨合約呼叫/創建、寫入未知地址，靜態分析無法框住存取集合，一律走串行。
var forbiddenParallelOps = map[byte]string{
	0xF0: "CREATE",
	0xF1: "CALL",
	0xF2: "CALLCODE",
	0xF4: "DELEGATECALL",
	0xF5: "CREATE2",
	0xFA: "STATICCALL",
}

// hasForbiddenOps 掃描位元組碼是否含禁止並行的 opcode。
// 注意：必須跳過 PUSH 資料區，避免把 PUSH 嵌入的資料位元組誤判為 opcode。
func hasForbiddenOps(code []byte) bool {
	for pc := 0; pc < len(code); {
		op := code[pc]
		if _, bad := forbiddenParallelOps[op]; bad {
			return true
		}
		// PUSH1..PUSH32 跳過資料區。
		if op >= 0x60 && op <= 0x7F {
			pc += 1 + int(op-0x5F)
		} else {
			pc++
		}
	}
	return false
}

// knownCallSelectors 為可解碼出「位址參數」的常見函式選擇器 → 位址參數 word 偏移
// （相對於 calldata 開頭，每個位址佔一個 32-byte word；位址取 word 末 20 byte）。
// 未知 selector 的呼叫保守地視為不可並行。
var knownCallSelectors = map[string][]int{
	// ERC20
	"a9059cbb": {4},     // transfer(address to,uint256)
	"23b872dd": {4, 36}, // transferFrom(address from,address to,uint256)
	"095ea7b3": {4},     // approve(address spender,uint256)
	"d09de08a": {4},     // increaseAllowance(address spender,uint256)
	"a457c2d7": {4},     // decreaseAllowance(address spender,uint256)
	// ERC721（approve(address to,uint256 tokenId) 與 ERC20 approve 同 selector 095ea7b3，
	// 位址參數同位置，沿用上方條目，不重複登記）
	"42842e0e": {4, 36}, // safeTransferFrom(address from,address to,uint256 id)
	"b88d4fde": {4, 36}, // safeTransferFrom(address from,address to,uint256 id,bytes data)
	// ERC1155
	"f242432a": {4, 36}, // safeTransferFrom(address from,address to,uint256 id,uint256 amount,bytes data)
	// 常見 mint
	"6a627846": {4}, // mint(address to,uint256 amount)
}

// wordAddr 從 calldata 的某 word 偏移解出規範化位址（word 末 20 byte）。
// 長度不足回空字串。
func wordAddr(calldata []byte, off int) string {
	if off+WordByteLen > len(calldata) {
		return ""
	}
	w := calldata[off : off+WordByteLen]
	return NormalizeAddress(w[12:32])
}

// CallParallelEligible 判斷一筆「合約呼叫」是否可進入並行 lane：
//   - 目標合約位元組碼不得含 CALL-family / CREATE-family opcode；
//   - calldata 前 4 byte selector 必須可解碼為已知函式（位址參數齊全）。
//
// 回傳 eligible 與「預測外部存取集合」（已規範化、不含目標合約本身——目標合約
// 由呼叫端隱式白名單）。caller/target 為已規範化的 0x 位址。
func CallParallelEligible(code []byte, caller, target string, calldata []byte) (eligible bool, predicted map[string]struct{}) {
	predicted = map[string]struct{}{}
	if caller == "" || target == "" {
		return false, predicted
	}
	predicted[NormalizeAddress(caller)] = struct{}{}
	// 目標合約本身加入 predicted（寫入它不算「預測外」），但不參與 layer 衝突。
	predicted[NormalizeAddress(target)] = struct{}{}

	if hasForbiddenOps(code) {
		return false, predicted
	}
	if len(calldata) < 4 {
		return false, predicted
	}
	sel := make([]byte, 4)
	copy(sel, calldata[:4])
	offs, ok := knownCallSelectors[hex4(sel)]
	if !ok {
		// 無法解碼 calldata → 保守串行。
		return false, predicted
	}
	for _, off := range offs {
		a := wordAddr(calldata, off)
		if a == "" {
			return false, predicted
		}
		predicted[a] = struct{}{}
	}
	return true, predicted
}

// hex4 把 4-byte selector 轉成小寫 hex。
func hex4(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, 8)
	for _, x := range b {
		out = append(out, hexd[x>>4], hexd[x&0xF])
	}
	return string(out)
}

// disjointSet 回傳兩個位址集合是否不相交。
func disjointSet(a, b map[string]struct{}) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	for k := range a {
		if _, ok := b[k]; ok {
			return false
		}
	}
	return true
}

// layerConflicts 判斷兩筆交易的預測集合是否衝突。
// 衝突判定只看「外部存取集合」：caller 與解碼出的位址參數。目標合約位址（集合中
// 必然存在的 target key）是共享執行上下文，刻意從衝突判定中剔除——同合約內對
// 不相交 storage slot 的並行寫入由事後 slot 級檢測把關（parallel_apply.go）。
func layerConflicts(predA, predB map[string]struct{}, targetA, targetB string) bool {
	for k := range predA {
		// 目標合約位址是共享執行上下文，不計外部衝突。
		if k == targetA || k == targetB {
			continue
		}
		if _, ok := predB[k]; ok {
			return true
		}
	}
	return false
}

// ScheduleLayers 以 greedy level scheduling 把交易切成有序 layer（保留原序）。
//   - n：交易數；pred[i]：第 i 筆的預測外部存取集合；targets[i]：第 i 筆目標合約位址。
//   - 回傳 layers：每層為原始交易索引（升序）；第 0 層全部先於第 1 層…
//   - 同一 layer 內任兩筆預測外部集合兩兩不相交。
func ScheduleLayers(n int, pred []map[string]struct{}, targets []string) [][]int {
	layers := [][]int{}
	for i := 0; i < n; i++ {
		placed := false
		for l := range layers {
			conflict := false
			for _, j := range layers[l] {
				if layerConflicts(pred[i], pred[j], targets[i], targets[j]) {
					conflict = true
					break
				}
			}
			if !conflict {
				layers[l] = append(layers[l], i)
				placed = true
				break
			}
		}
		if !placed {
			layers = append(layers, []int{i})
		}
	}
	return layers
}

// sortedKeys 把集合 keys 排序（確保輸出確定性，便於測試比較與日誌）。
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
