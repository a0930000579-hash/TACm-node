package vm

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// TACWallet 鏈上多簽錢包（P1-②）：Safe 風格 N/M 門檻合約錢包＋EIP-1271 合約簽名驗證。
// 用途：節點獎勵金庫、社群基金、橋金庫可用多簽管理；錢包/交易所可透過
// isValidSignature 驗證合約簽名（主流鏈 Gnosis Safe / Argent 同級能力）。
//
// Slot 佈局（部署者 slot10 約定與 ERC-20/721/tacnames 一致）：
//
//	slot1  = ownerCount（目前 owner 數）
//	slot2  = threshold（簽名門檻，1 ≤ threshold ≤ ownerCount）
//	slot3  = nonce（已執行交易數，防重放）
//	slot4  = mapping(owner → bool)          // 持有人集合
//	slot5  = mapping(txHash → approvals)    // 交易已收集簽名數
//	slot6  = mapping(txHash → mapping(signer → bool)) // 防重複簽名
//	slot10 = 部署者（管理員：可增刪 owner / 調門檻）
//
// 交易模型（固定 calldata，避免動態 bytes 彙編複雜度）：
// txHash = keccak256(to ‖ value ‖ nonce)；任一 owner 以 approveTransaction 附
// ECDSA 簽名（v/r/s 固定參數）批准；合約內 ecrecover 驗證簽名者為 owner 且
// 未重複，收集達 threshold 後自動 CALL(to, value) 執行並 nonce++。
// EIP-1271：isValidSignature(hash, v, r, s) 驗證簽名者為 owner 後回傳
// magic value 0x1626ba7e（固定參數版，selector 自訂避免與標準動態 ABI 衝突）。

var (
	SelectorWalletAddOwner        = []byte{0x70, 0x65, 0xcb, 0x48} // addOwner(address)
	SelectorWalletRemoveOwner     = []byte{0x17, 0x38, 0x25, 0xd9} // removeOwner(address)
	SelectorWalletChangeThreshold = []byte{0x69, 0x4e, 0x80, 0xc3} // changeThreshold(uint256)
	SelectorWalletIsOwner         = []byte{0x2f, 0x54, 0xbf, 0x6e} // isOwner(address)
	SelectorWalletOwnerCount      = []byte{0x0d, 0xb0, 0x26, 0x22} // ownerCount()
	SelectorWalletThreshold       = []byte{0x42, 0xcd, 0xe4, 0xe8} // threshold()
	SelectorWalletSubmit          = []byte{0xa8, 0x8a, 0x4c, 0x89} // submitTransaction(address,uint256,uint256)
	SelectorWalletApprove         = []byte{0x62, 0x54, 0xda, 0x56} // approveTransaction(address,uint256,uint256,uint8,bytes32,bytes32)
	SelectorWalletIsValidSig      = []byte{0x8b, 0x25, 0x7d, 0x3d} // isValidSignature(bytes32,uint8,bytes32,bytes32)
	SelectorWalletIsApproved      = []byte{0xc8, 0x55, 0x1f, 0x9f} // isApproved(address,uint256,uint256,address)
	SelectorWalletApprovals       = []byte{0x44, 0xfa, 0x66, 0x83} // approvals(address,uint256,uint256)
	SelectorWalletSupportsIf      = []byte{0x01, 0xff, 0xc9, 0xa7} // supportsInterface(bytes4)
)

// codeCalldata132/164：approveTransaction/isValidSignature 需讀取更深 calldata
// 偏移（codeCalldata100 已在 erc1155.go 定義）。
var (
	codeCalldata132 = []byte{0x60, 0x84, 0x35} // CALLDATALOAD(132)（r@132）
	codeCalldata164 = []byte{0x60, 0xA4, 0x35} // CALLDATALOAD(164)（s@164）
)

// walletMagicValue：EIP-1271 回傳 magic value 0x1626ba7e（bytes4，左對齊 32 bytes）。
var walletMagicValue = []byte{0x63, 0x16, 0x26, 0xba, 0x7e, 0x60, 0xE0, 0x1B} // PUSH4 0x1626ba7e; PUSH1 0xE0; SHL

// callPrecompileECRecover 產生「CALL(0x01, ecrecover)」片段：
// 假設 memory[0..128]=hash‖v‖r‖s；輸出寫 memory[128..160]，CALL 回傳 success。
// EVM 慣例：outSize 先 push（棧底）、gas 最後 push（棧頂）。
var callPrecompileECRecover = []byte{
	0x60, 0x20, // PUSH1 outSize=32
	0x60, 0x80, // PUSH1 outOffset=128
	0x60, 0x80, // PUSH1 inSize=128
	0x60, 0x00, // PUSH1 inOff=0
	0x60, 0x00, // PUSH1 value=0
	0x60, 0x01, // PUSH1 to=1
	0x61, 0xFF, 0xFF, // PUSH2 gas=0xFFFF
	0xF1, // CALL
}

// codeExecuteCall 產生「CALL(to, value)」片段（data 空）：假設 to/value 取自
// calldata 4/36；CALL 回傳 success。EVM 慣例：outSize 先 push、gas 最後 push。
var codeExecuteCall = []byte{
	0x60, 0x00, // PUSH1 outSize=0
	0x60, 0x00, // PUSH1 outOffset=0
	0x60, 0x00, // PUSH1 inSize=0
	0x60, 0x00, // PUSH1 inOff=0
	0x60, 0x24, 0x35, // PUSH1 36 CALLDATALOAD → value
	0x60, 0x04, 0x35, // PUSH1 4 CALLDATALOAD → to
	0x61, 0xFF, 0xFF, // PUSH2 gas=0xFFFF
	0xF1, // CALL
}

// TACWalletRuntime 回傳多簽錢包 runtime 位元組碼（固定）。
func TACWalletRuntime() []byte {
	selOrder := [][]byte{
		SelectorWalletAddOwner, SelectorWalletRemoveOwner, SelectorWalletChangeThreshold,
		SelectorWalletIsOwner, SelectorWalletOwnerCount, SelectorWalletThreshold,
		SelectorWalletSubmit, SelectorWalletApprove, SelectorWalletIsValidSig,
		SelectorWalletIsApproved, SelectorWalletApprovals, SelectorWalletSupportsIf,
	}
	header := []byte{0x60, 0x00, 0x35} // CALLDATALOAD(0)
	header = append(header, push32(new(big.Int).Lsh(big.NewInt(1), 224))...)
	header = append(header, 0x04) // DIV → sel4
	type jumpPatch struct{ pos, label int }
	var patches []jumpPatch
	d := append([]byte{}, header...)
	for i, sel := range selOrder {
		d = append(d, 0x80) // DUP1
		d = append(d, push32(selNum(sel))...)
		d = append(d, 0x14, 0x61, 0x00, 0x00, 0x57) // EQ / PUSH2 dst / JUMPI
		patches = append(patches, jumpPatch{pos: len(d) - 3, label: i})
	}
	d = append(d, 0x60, 0x00, 0x60, 0x00, 0xF3) // fallback RETURN(0,0)

	segs := []builtSeg{
		tacWalletSegAddOwner(), tacWalletSegRemoveOwner(), tacWalletSegChangeThreshold(),
		tacWalletSegIsOwner(), tacWalletSegOwnerCount(), tacWalletSegThreshold(),
		tacWalletSegSubmit(), tacWalletSegApprove(), tacWalletSegIsValidSig(),
		tacWalletSegIsApproved(), tacWalletSegApprovals(), tacWalletSegSupportsIf(),
	}
	labs := make([]int, len(segs))
	off := len(d)
	for i, sg := range segs {
		labs[i] = off
		off += len(sg.code)
	}
	for _, p := range patches {
		d[p.pos] = byte(labs[p.label] >> 8)
		d[p.pos+1] = byte(labs[p.label] & 0xFF)
	}
	for i, sg := range segs {
		for _, j := range sg.jumps {
			abs := labs[i] + j.target
			sg.code[j.pos] = byte(abs >> 8)
			sg.code[j.pos+1] = byte(abs & 0xFF)
		}
	}
	for _, sg := range segs {
		d = append(d, sg.code...)
	}
	return d
}

// TACWalletInit 產生部署 init：記錄部署者到 slot10，CODECOPY runtime 並 RETURN。
func TACWalletInit() []byte {
	runtime := TACWalletRuntime()
	var b []byte
	b = append(b, codeCaller...) // CALLER
	b = append(b, 0x60, 0x0A)    // key 10
	b = append(b, 0x55)          // SSTORE slot10=部署者
	initLen := len(b)
	rl := len(runtime)
	src := initLen + 15
	b = append(b, 0x61, byte(rl>>8), byte(rl&0xFF))                   // PUSH2 size
	b = append(b, 0x61, byte(src>>8), byte(src&0xFF))                 // PUSH2 src
	b = append(b, 0x60, 0x00)                                         // PUSH dest
	b = append(b, 0x39)                                               // CODECOPY
	b = append(b, 0x61, byte(rl>>8), byte(rl&0xFF), 0x60, 0x00, 0xF3) // RETURN(0, rl)
	return append(b, runtime...)
}

// nthJumpDest 找第 n 個（0 起）JUMPDEST 的段內 index，供多跳轉目標回填。
func nthJumpDest(b []byte, n int) int {
	cnt := 0
	for i := 0; i < len(b); i++ {
		if b[i] == 0x5B {
			if cnt == n {
				return i
			}
			cnt++
		}
	}
	return -1
}

// walletOwnerSlot 產生「mapping(owner→bool)」slot 計算（key 在 stack 頂）。
func walletOwnerSlot(keyCode []byte) []byte {
	var b []byte
	b = append(b, keyCode...)
	b = append(b, 0x60, 0x00, 0x52)             // MSTORE(0, key)
	b = append(b, 0x60, 0x04, 0x60, 0x20, 0x52) // MSTORE(32, 4)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20) // SHA3(0,64)
	return b
}

// walletTxHashCode 產生 txHash=keccak(to@4,value@36,nonce@68) 計算（結果在 stack）。
// memory 佈局：先寫入 [0..96]，SHA3(0,96)。
var walletTxHashCode = []byte{
	0x60, 0x04, 0x35, 0x60, 0x00, 0x52, // MSTORE(0, to)
	0x60, 0x24, 0x35, 0x60, 0x20, 0x52, // MSTORE(32, value)
	0x60, 0x44, 0x35, 0x60, 0x40, 0x52, // MSTORE(64, nonce)
	0x60, 0x60, 0x60, 0x00, 0x20, // SHA3(0,96) → txHash
}

// tacWalletSegAddOwner: addOwner(address)——部署者 only；owner 不存在才加入。
func tacWalletSegAddOwner() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, 0x60, 0x0A, 0x54)                   // SLOAD(10) → [deployer]
	b = append(b, codeCaller...)                       // [deployer, caller]
	b = append(b, 0x14)                               // EQ → [eq]
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI dOk
	jumps = append(jumps, segJump{pos: okPos, target: 1})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（非部署者）
	b = append(b, 0x5B)                               // dOk:
	b = append(b, codeCalldata4...)                    // [owner]
	b = append(b, 0x80)                               // [owner, owner]
	b = append(b, walletOwnerSlot(nil)...)            // [owner, slotO]
	b = append(b, 0x54)                               // [owner, isOwner]
	b = append(b, 0x15)                               // [owner, !isOwner]
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI add
	jumps = append(jumps, segJump{pos: ok2Pos, target: 2})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（已存在）
	b = append(b, 0x5B)                               // add:
	b = append(b, 0x50)                               // POP owner
	b = append(b, 0x60, 0x01)                         // PUSH1 1（value）
	b = append(b, codeCalldata4...)                    // [1, owner]
	b = append(b, walletOwnerSlot(nil)...)            // [1, slotO]
	b = append(b, 0x55)                               // SSTORE mapping[owner]=1
	b = append(b, 0x60, 0x01, 0x54)                   // SLOAD(1) → [count]
	b = append(b, 0x60, 0x01, 0x01)                   // ADD1 → [count+1]
	b = append(b, 0x60, 0x01, 0x55)                   // SSTORE(1, count+1)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	dOkIdx := nthJumpDest(b, 1)
	addIdx := nthJumpDest(b, 2)
	for i := range jumps {
		if i == 0 {
			jumps[i].target = dOkIdx
		} else {
			jumps[i].target = addIdx
		}
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacWalletSegRemoveOwner: removeOwner(address)——部署者 only；移除後若
// threshold>ownerCount 自動校正 threshold=ownerCount。
func tacWalletSegRemoveOwner() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, 0x60, 0x0A, 0x54)                   // SLOAD(10)
	b = append(b, codeCaller...)
	b = append(b, 0x14)                               // EQ
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI dOk
	jumps = append(jumps, segJump{pos: okPos, target: 1})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	b = append(b, 0x5B)                               // dOk:
	b = append(b, codeCalldata4...)                    // [owner]
	b = append(b, 0x80)                               // [owner, owner]
	b = append(b, walletOwnerSlot(nil)...)            // [owner, slotO]
	b = append(b, 0x54)                               // [owner, isOwner]
	b = append(b, 0x15)                               // [owner, !isOwner]
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI end（不存在 → 結束）
	jumps = append(jumps, segJump{pos: ok2Pos, target: 2})
	b = append(b, 0x50)                               // POP owner（存在 → 移除）
	b = append(b, 0x60, 0x00)                         // PUSH1 0（value）
	b = append(b, codeCalldata4...)                    // [0, owner]
	b = append(b, walletOwnerSlot(nil)...)            // [0, slotO]
	b = append(b, 0x55)                               // SSTORE mapping[owner]=0
	b = append(b, 0x60, 0x01, 0x54)                   // SLOAD(1) → [count]
	b = append(b, 0x60, 0x01, 0x03)                   // SUB1 → [count-1]
	b = append(b, 0x60, 0x01, 0x55)                   // SSTORE(1, count-1)
	b = append(b, 0x60, 0x02, 0x54)                   // SLOAD(2) → [th]
	b = append(b, 0x60, 0x01, 0x54)                   // [th, c]
	b = append(b, 0x11)                               // GT → [th>c]
	b = append(b, 0x15)                               // ISZERO → [th<=c]
	ok3Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI end（th<=c → 跳過校正）
	jumps = append(jumps, segJump{pos: ok3Pos, target: 2})
	b = append(b, 0x60, 0x01, 0x54)                   // SLOAD(1) → [c]
	b = append(b, 0x60, 0x02, 0x55)                   // SSTORE(2, c)
	b = append(b, 0x5B)                               // end:
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	dOkIdx := nthJumpDest(b, 1)
	endIdx := nthJumpDest(b, 2)
	for i := range jumps {
		jumps[i].target = endIdx
		if i == 0 {
			jumps[i].target = dOkIdx
		}
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacWalletSegChangeThreshold: changeThreshold(uint256)——部署者 only；
// 1 ≤ t ≤ ownerCount。
func tacWalletSegChangeThreshold() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, 0x60, 0x0A, 0x54)                   // SLOAD(10)
	b = append(b, codeCaller...)
	b = append(b, 0x14)                               // EQ
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI dOk
	jumps = append(jumps, segJump{pos: okPos, target: 1})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	b = append(b, 0x5B)                               // dOk:
	b = append(b, codeCalldata4...)                    // [t]
	b = append(b, 0x15)                               // [t==0]
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI bad（t==0）
	jumps = append(jumps, segJump{pos: ok2Pos, target: 2})
	b = append(b, codeCalldata4...)                    // [t]
	b = append(b, 0x60, 0x01, 0x54)                   // [t, c]
	b = append(b, 0x11)                               // GT → [t>c]
	b = append(b, 0x15)                               // [t<=c]
	ok3Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: ok3Pos, target: 3})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（t>count 或 t==0）
	b = append(b, 0x5B)                               // ok:
	b = append(b, codeCalldata4...)                    // [t]
	b = append(b, 0x60, 0x02, 0x55)                   // SSTORE(2, t)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	b = append(b, 0x5B)                               // bad:
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	dOkIdx := nthJumpDest(b, 1)
	badIdx := nthJumpDest(b, 3)
	okIdx := nthJumpDest(b, 2)
	for i := range jumps {
		switch i {
		case 0:
			jumps[i].target = dOkIdx
		case 1:
			jumps[i].target = badIdx
		case 2:
			jumps[i].target = okIdx
		}
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacWalletSegIsOwner: isOwner(address)——回傳 mapping(owner→bool)。
func tacWalletSegIsOwner() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, codeCalldata4...)
	b = append(b, walletOwnerSlot(nil)...)
	b = append(b, 0x54) // SLOAD
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacWalletSegOwnerCount: ownerCount()——回傳 slot1。
func tacWalletSegOwnerCount() builtSeg {
	var b []byte
	b = append(b, 0x5B)          // JUMPDEST
	b = append(b, 0x60, 0x01, 0x54) // SLOAD(1)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacWalletSegThreshold: threshold()——回傳 slot2。
func tacWalletSegThreshold() builtSeg {
	var b []byte
	b = append(b, 0x5B)          // JUMPDEST
	b = append(b, 0x60, 0x02, 0x54) // SLOAD(2)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacWalletSegSubmit: submitTransaction(to,value,nonce)——owner only；回傳 txHash。
func tacWalletSegSubmit() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, codeCaller...)                       // [caller]
	b = append(b, walletOwnerSlot(nil)...)            // [slotO]
	b = append(b, 0x54)                               // [isOwner]
	b = append(b, 0x15)                               // [!isOwner]
	failPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail（非 owner）
	jumps = append(jumps, segJump{pos: failPos, target: 1})
	b = append(b, walletTxHashCode...)                 // owner：計算 txHash
	b = append(b, RET32...)
	b = append(b, 0x5B)                               // fail:
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	failIdx := nthJumpDest(b, 1)
	for i := range jumps {
		jumps[i].target = failIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacWalletSegApprove: approveTransaction(to,value,nonce,v,r,s)——任一 owner
// 以 ECDSA 簽名批准；合約內 ecrecover 驗證簽名者為 owner、未重複；收集達
// threshold 後 CALL(to, value) 執行並 nonce++。CALL 失敗 revert 整筆。
// memory 佈局：[0..32]=txHash（ecrecover hash）[32..64]=v [64..96]=r
// [96..128]=s [128..160]=signer(out) [160..192]=txHash 備份 [192..224]=signer 備份
// [224..256]=approvals+1 暫存。
func tacWalletSegApprove() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B) // JUMPDEST
	// --- 1. txHash = keccak(to,value,nonce) → 備份 memory[160]、memory[0] ---
	b = append(b, walletTxHashCode...)
	b = append(b, 0x60, 0xA0, 0x52)                  // MSTORE(160, txHash) 備份
	b = append(b, 0x60, 0xA0, 0x51)                  // MLOAD(160) → [txHash]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)（ecrecover hash）
	// --- 2. v/r/s → memory ---
	b = append(b, codeCalldata100...)                 // v
	b = append(b, 0x60, 0x20, 0x52)                  // MSTORE(32, v)
	b = append(b, codeCalldata132...)                 // r
	b = append(b, 0x60, 0x40, 0x52)                  // MSTORE(64, r)
	b = append(b, codeCalldata164...)                 // s
	b = append(b, 0x60, 0x60, 0x52)                  // MSTORE(96, s)
	// --- 3. ecrecover precompile ---
	b = append(b, callPrecompileECRecover...)
	b = append(b, 0x15)                               // !success
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail
	jumps = append(jumps, segJump{pos: okPos, target: 4})
	// --- 4. signer 備份 memory[192]；owner 檢查 ---
	b = append(b, 0x60, 0x80, 0x51)                  // MLOAD(128) → [signer]
	b = append(b, 0x60, 0xC0, 0x52)                  // MSTORE(192, signer)
	b = append(b, 0x60, 0xC0, 0x51)                  // MLOAD(192) → [signer]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, signer)
	b = append(b, 0x60, 0x04, 0x60, 0x20, 0x52)      // MSTORE(32, 4)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotO]
	b = append(b, 0x54)                              // SLOAD → [isOwner]
	b = append(b, 0x15)                              // !isOwner
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail
	jumps = append(jumps, segJump{pos: ok2Pos, target: 4})
	// --- 5. 防重複：slotB = nested(txHash, signer, 6) ---
	b = append(b, 0x60, 0xA0, 0x51)                  // MLOAD(160) → [txHash]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)
	b = append(b, 0x60, 0x06, 0x60, 0x20, 0x52)      // MSTORE(32, 6)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [inner]
	b = append(b, 0x60, 0xC0, 0x51)                  // [inner, signer]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, signer)
	b = append(b, 0x60, 0x20, 0x52)                  // MSTORE(32, inner)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotB]
	b = append(b, 0x54)                              // SLOAD → [dup]
	ok3Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail（dup=1 已批准 → fail）
	jumps = append(jumps, segJump{pos: ok3Pos, target: 4})
	// --- 6. 標記已批准：slotB = nested(txHash, signer, 6) → SSTORE(slotB, 1) ---
	b = append(b, 0x60, 0xA0, 0x51)                  // MLOAD(160) → [txHash]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)
	b = append(b, 0x60, 0x06, 0x60, 0x20, 0x52)      // MSTORE(32, 6)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [inner]
	b = append(b, 0x60, 0xC0, 0x51)                  // [inner, signer]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, signer)
	b = append(b, 0x60, 0x20, 0x52)                  // MSTORE(32, inner)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotB]
	b = append(b, 0x60, 0x01)                        // PUSH1 1（value）→ [slotB, 1]
	b = append(b, 0x90)                              // SWAP1 → [1, slotB]（棧頂 key）
	b = append(b, 0x55)                              // SSTORE(slotB, 1)
	// --- 7. approvals++：slotA = map(txHash,5) ---
	b = append(b, 0x60, 0xA0, 0x51)                  // MLOAD(160)
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)
	b = append(b, 0x60, 0x05, 0x60, 0x20, 0x52)      // MSTORE(32, 5)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotA]
	b = append(b, 0x54)                              // SLOAD → [count]
	b = append(b, 0x60, 0x01, 0x01)                  // ADD1 → [count+1]
	b = append(b, 0x60, 0xE0, 0x52)                  // MSTORE(224, count+1) 暫存
	b = append(b, 0x60, 0xA0, 0x51)                  // MLOAD(160)
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)
	b = append(b, 0x60, 0x05, 0x60, 0x20, 0x52)      // MSTORE(32, 5)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotA]
	b = append(b, 0x60, 0xE0, 0x51)                  // MLOAD(224) → [slotA, count+1]
	b = append(b, 0x90)                              // SWAP1 → [count+1, slotA]（棧頂 key）
	b = append(b, 0x55)                              // SSTORE(slotA, count+1)
	// --- 8. 達門檻？ count+1 >= threshold → exec ---
	b = append(b, 0x60, 0xE0, 0x51)                  // MLOAD(224) → [count+1]
	b = append(b, 0x60, 0x02, 0x54)                  // [count+1, th]
	b = append(b, 0x10)                              // LT → [count+1 < th]
	b = append(b, 0x15)                              // ISZERO → [count+1 >= th]
	ok4Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI exec
	jumps = append(jumps, segJump{pos: ok4Pos, target: 5})
	// 未達標：回 true（已記錄簽名）
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	// --- 9. exec：CALL(to, value) ---
	b = append(b, 0x5B)                               // exec:
	b = append(b, codeExecuteCall...)
	b = append(b, 0x15)                               // !success
	ok5Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail
	jumps = append(jumps, segJump{pos: ok5Pos, target: 4})
	// 執行成功：nonce++（slot3）
	b = append(b, 0x60, 0x03, 0x54)                   // SLOAD(3) → [n]
	b = append(b, 0x60, 0x01, 0x01)                   // ADD1 → [n+1]
	b = append(b, 0x60, 0x03, 0x55)                   // SSTORE(3, n+1)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	// --- 10. fail: REVERT ---
	b = append(b, 0x5B)                               // fail:
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	failIdx := nthJumpDest(b, 2)
	execIdx := nthJumpDest(b, 1)
	for i := range jumps {
		if i == 3 {
			jumps[i].target = execIdx
		} else {
			jumps[i].target = failIdx
		}
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacWalletSegIsValidSig: isValidSignature(hash,v,r,s)——ecrecover 驗證簽名者
// 為 owner 後回傳 magic value 0x1626ba7e（EIP-1271）。
func tacWalletSegIsValidSig() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B) // JUMPDEST
	// hash@4、v@36、r@68、s@100 → memory[0..128]
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x00, 0x52)                 // MSTORE(0, hash)
	b = append(b, codeCalldata24...)
	b = append(b, 0x60, 0x20, 0x52)                 // MSTORE(32, v)
	b = append(b, codeCalldata68...)
	b = append(b, 0x60, 0x40, 0x52)                 // MSTORE(64, r)
	b = append(b, codeCalldata100...)
	b = append(b, 0x60, 0x60, 0x52)                 // MSTORE(96, s)
	b = append(b, callPrecompileECRecover...)
	b = append(b, 0x15)                               // !success
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail
	jumps = append(jumps, segJump{pos: okPos, target: 2})
	b = append(b, 0x60, 0x80, 0x51)                  // MLOAD(128) → [signer]
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, signer)
	b = append(b, 0x60, 0x04, 0x60, 0x20, 0x52)      // MSTORE(32, 4)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotO]
	b = append(b, 0x54)                              // SLOAD → [isOwner]
	b = append(b, 0x15)                              // !isOwner
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI fail
	jumps = append(jumps, segJump{pos: ok2Pos, target: 2})
	// magic value 左對齊 32 bytes → RETURN(0,32)
	b = append(b, walletMagicValue...)
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, magic32)
	b = append(b, 0x60, 0x20, 0x60, 0x00, 0xF3)      // RETURN(0,32)
	b = append(b, 0x5B)                               // fail:
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	failIdx := nthJumpDest(b, 1)
	for i := range jumps {
		jumps[i].target = failIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacWalletSegIsApproved: isApproved(to,value,nonce,signer)——回傳 nested 標記。
func tacWalletSegIsApproved() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, walletTxHashCode...)
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)
	b = append(b, 0x60, 0x06, 0x60, 0x20, 0x52)      // MSTORE(32, 6)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [inner]
	b = append(b, codeCalldata100...)
	b = append(b, 0x60, 0x00, 0x52)                 // MSTORE(0, signer@100)
	b = append(b, 0x60, 0x20, 0x52)                  // MSTORE(32, inner)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotB]
	b = append(b, 0x54)                              // SLOAD
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacWalletSegApprovals: approvals(to,value,nonce)——回傳已收集簽名數。
func tacWalletSegApprovals() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, walletTxHashCode...)
	b = append(b, 0x60, 0x00, 0x52)                  // MSTORE(0, txHash)
	b = append(b, 0x60, 0x05, 0x60, 0x20, 0x52)      // MSTORE(32, 5)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)      // SHA3 → [slotA]
	b = append(b, 0x54)                              // SLOAD
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacWalletSegSupportsIf: supportsInterface(bytes4)——回傳 0（本合約暫不宣告 165）。
func tacWalletSegSupportsIf() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, 0x60, 0x00) // PUSH1 0
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// ---- 對外 calldata 建構（node RPC / 前端使用）----

// walletCalldata 組裝「selector＋固定 32-byte 參數」calldata。
func walletCalldata(sel []byte, args ...*big.Int) []byte {
	b := append([]byte{}, sel...)
	for _, a := range args {
		b = append(b, IntToBytes(a, 32)...)
	}
	return b
}

// walletOwnerWord 把 0x 20-byte 地址轉為 32-byte 值（與合約內 ecrecover signer 一致）。
func walletOwnerWord(owner0x string) (*big.Int, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(owner0x), "0x"))
	if err != nil || len(b) != 20 {
		return nil, fmt.Errorf("tacwallet: owner 需為 0x 開頭 20-byte 地址")
	}
	return BytesToInt(b), nil
}

// TACWalletAddOwnerCalldata：addOwner(address)。
func TACWalletAddOwnerCalldata(owner0x string) ([]byte, error) {
	w, err := walletOwnerWord(owner0x)
	if err != nil {
		return nil, err
	}
	return walletCalldata(SelectorWalletAddOwner, w), nil
}

// TACWalletRemoveOwnerCalldata：removeOwner(address)。
func TACWalletRemoveOwnerCalldata(owner0x string) ([]byte, error) {
	w, err := walletOwnerWord(owner0x)
	if err != nil {
		return nil, err
	}
	return walletCalldata(SelectorWalletRemoveOwner, w), nil
}

// TACWalletChangeThresholdCalldata：changeThreshold(uint256)。
func TACWalletChangeThresholdCalldata(t int64) []byte {
	return walletCalldata(SelectorWalletChangeThreshold, big.NewInt(t))
}

// TACWalletSubmitCalldata：submitTransaction(to,value,nonce)。
func TACWalletSubmitCalldata(to, value, nonce *big.Int) []byte {
	return walletCalldata(SelectorWalletSubmit, to, value, nonce)
}

// TACWalletApproveCalldata：approveTransaction(to,value,nonce,v,r,s)。
func TACWalletApproveCalldata(to, value, nonce, v, r, s *big.Int) []byte {
	return walletCalldata(SelectorWalletApprove, to, value, nonce, v, r, s)
}

// TACWalletIsValidSigCalldata：isValidSignature(hash,v,r,s)。
func TACWalletIsValidSigCalldata(hash []byte, v, r, s *big.Int) []byte {
	return walletCalldata(SelectorWalletIsValidSig, BytesToInt(hash), v, r, s)
}

// TACWalletIsOwnerCalldata：isOwner(address)。
func TACWalletIsOwnerCalldata(owner0x string) ([]byte, error) {
	w, err := walletOwnerWord(owner0x)
	if err != nil {
		return nil, err
	}
	return walletCalldata(SelectorWalletIsOwner, w), nil
}

// TACWalletOwnerCountCalldata：ownerCount()。
func TACWalletOwnerCountCalldata() []byte {
	return walletCalldata(SelectorWalletOwnerCount)
}

// TACWalletThresholdCalldata：threshold()。
func TACWalletThresholdCalldata() []byte {
	return walletCalldata(SelectorWalletThreshold)
}

// TACWalletApprovalsCalldata：approvals(to,value,nonce)。
func TACWalletApprovalsCalldata(to, value, nonce *big.Int) []byte {
	return walletCalldata(SelectorWalletApprovals, to, value, nonce)
}

// TACWalletIsApprovedCalldata：isApproved(to,value,nonce,signer)。
func TACWalletIsApprovedCalldata(to, value, nonce *big.Int, signer0x string) ([]byte, error) {
	w, err := walletOwnerWord(signer0x)
	if err != nil {
		return nil, err
	}
	return walletCalldata(SelectorWalletIsApproved, to, value, nonce, w), nil
}
