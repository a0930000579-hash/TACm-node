package vm

import "math/big"

// 本檔提供「鏈上標準代幣（ERC-20 風格）」的完整位元組碼與輔助函數，
// 讓任何地址可在 TAC 鏈上部署自有代幣（totalSupply 由部署者於構造時指定，
// 初始供應量全部結給部署者），並支援 totalSupply / balanceOf / transfer /
// name / symbol 五項標準介面（M43＋M44）。
//
// Storage 配置（標準 EVM slot 模型）：
//
//	slot0                = totalSupply
//	slot1                = name（右對齊 32-byte）
//	slot2                = symbol（右對齊 32-byte）
//	slot[address]        = balance（以 256-bit 整數為 key）
//
// Runtime 採用 selector dispatch（CALLDATALOAD(0) 後 DIV 2^224 只取前
// 4-byte selector 與常數比較），比中後 JUMP 至對應實作。

// ERC-20 標準 function selector（4-byte，右對齊於 32-byte 字）。
var (
	SelectorTotalSupply   = []byte{0x18, 0x16, 0x0d, 0xdd} // totalSupply()
	SelectorBalanceOf     = []byte{0x70, 0xa0, 0x82, 0x31} // balanceOf(address)
	SelectorTransfer      = []byte{0xa9, 0x05, 0x9c, 0xbb} // transfer(address,uint256)
	SelectorName          = []byte{0x06, 0xfd, 0xde, 0x03} // name()
	SelectorSymbol        = []byte{0x95, 0xd8, 0x9b, 0x41} // symbol()
	SelectorApprove       = []byte{0x09, 0x5e, 0xa7, 0xb3} // approve(address,uint256)
	SelectorAllowance     = []byte{0xdd, 0x62, 0xed, 0x3e} // allowance(address,address)
	SelectorTransferFrom  = []byte{0x23, 0xb8, 0x72, 0xdd} // transferFrom(address,address,uint256)
	SelectorDecimals      = []byte{0x31, 0x3c, 0xe5, 0x67} // decimals()
	// 標準事件 topic0（keccak256 簽名）——與乙太坊主網一致。
	TopicTransfer = []byte{0xdd, 0xf2, 0x52, 0xad, 0x1b, 0xe2, 0xc8, 0x9b,
		0x69, 0xc2, 0xb0, 0x68, 0xfc, 0x37, 0x8d, 0xaa, 0x95, 0x2b, 0xa7, 0xf1,
		0x63, 0xc4, 0xa1, 0x16, 0x28, 0xf5, 0x5a, 0x4d, 0xf5, 0x23, 0xb3, 0xef}
	TopicApproval = []byte{0x8c, 0x5b, 0xe1, 0xe5, 0xeb, 0xec, 0x7d, 0x5b,
		0xd1, 0x4f, 0x71, 0x42, 0x7d, 0x1e, 0x84, 0xf3, 0xdd, 0x03, 0x14, 0xc0,
		0xf7, 0xb2, 0x29, 0x1e, 0x5b, 0x20, 0x0a, 0xc8, 0xc7, 0xc3, 0xb9, 0x25}
	// allowance 嵌套映射基礎 slot（自訂布局：keccak(spender‖keccak(owner‖3))）。
	AllowanceBaseSlot = big.NewInt(3)
)

// RET32 為「值 MSTORE@0、RETURN(0,32)」尾碼。
var RET32 = []byte{0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3}

// push32 產生 PUSH32 <32-byte 右對齊值>。
func push32(x *big.Int) []byte {
	out := []byte{0x7F}
	return append(out, IntToBytes(x, 32)...)
}

// selNum 把 4-byte selector 轉為右對齊數值（DIV 2^224 後可直接 EQ）。
func selNum(sel []byte) *big.Int {
	return new(big.Int).SetBytes(sel)
}

// Erc20Runtime 回傳標準 ERC-20 runtime 位元組碼（固定）。
// selector 解析：CALLDATALOAD(0) 後 DIV 2^224 只取前 4 bytes（避免參數
// 污染 32-byte 比較）；每個比較前 DUP1 保留 sel4；JUMPI 棧頂 dest、次 cond。
// 跳轉目標統一以 PUSH2（0x61）編碼（段偏移由收集器自動計算，避免手寫偏移出錯）。
//
// 支援介面（與乙太坊/BSC 標準一致）：
//   - totalSupply() / balanceOf(address) / name() / symbol() / decimals()
//   - transfer(address,uint256)（含餘額檢查、Transfer 事件）
//   - approve(address,uint256)（寫 allowance、Approval 事件）
//   - allowance(address,address)（讀嵌套映射）
//   - transferFrom(address,address,uint256)（授權轉帳、餘額檢查、Transfer 事件）
//
// Storage 配置（標準 slot 模型）：
//
//	slot0                = totalSupply
//	slot1                = name（右對齊 32-byte）
//	slot2                = symbol（右對齊 32-byte）
//	slot[address]        = balance（以 256-bit 整數為 key）
//	allowance(owner,spender) = keccak(spender ‖ keccak(owner ‖ 3))（嵌套映射）
func Erc20Runtime() []byte {
	// ---- dispatch：9 個 selector 比較對（統一 39 bytes：80 7F<sel> 14 61<dst> 57）＋fallback ----
	selOrder := [][]byte{
		SelectorTotalSupply, SelectorBalanceOf, SelectorTransfer,
		SelectorName, SelectorSymbol,
		SelectorApprove, SelectorAllowance, SelectorTransferFrom, SelectorDecimals,
	}
	header := []byte{0x60, 0x00, 0x35} // CALLDATALOAD(0)
	header = append(header, push32(new(big.Int).Lsh(big.NewInt(1), 224))...)
	header = append(header, 0x04) // DIV → sel4
	// jump 位置清單：[byte 位置, 目標 label 索引]（收集器最後統一 patch）。
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

	// ---- 9 個實作段（各段回傳自身 code＋相對跳轉清單）----
	segs := []builtSeg{
		erc20SegTotalSupply(), erc20SegBalanceOf(), erc20SegTransfer(),
		erc20SegName(), erc20SegSymbol(), erc20SegApprove(), erc20SegAllowance(),
		erc20SegTransferFrom(), erc20SegDecimals(),
	}
	// 計算各段絕對偏移。
	labs := make([]int, len(segs))
	off := len(d)
	for i, sg := range segs {
		labs[i] = off
		off += len(sg.code)
	}
	// patch dispatch 跳轉目標（絕對位置）。
	for _, p := range patches {
		d[p.pos] = byte(labs[p.label] >> 8)
		d[p.pos+1] = byte(labs[p.label] & 0xFF)
	}
	// patch 段內跳轉（labs[i]+相對位置）。
	for i, sg := range segs {
		for _, j := range sg.jumps {
			abs := labs[i] + j.target
			sg.code[j.pos] = byte(abs >> 8)
			sg.code[j.pos+1] = byte(abs & 0xFF)
		}
	}
	// 依序串接。
	for _, sg := range segs {
		d = append(d, sg.code...)
	}
	return d
}

// segJump 記錄段內 JUMPI 目標（位置與目標皆為相對段頭偏移；由收集器轉為絕對）。
type segJump struct{ pos, target int }

// builtSeg 為一段 runtime 位元組碼與其待 patch 的跳轉清單。
type builtSeg struct {
	code  []byte
	jumps []segJump
}

// allowSlotCode 產生「計算 allowance 嵌套映射 slot」的 bytecode 片段：
// mem[0]=owner、mem[32]=baseSlot(3) → SHA3(0,64)=inner → mem[0]=inner、
// mem[32]=spender → SHA3(0,64)=slot（棧頂回傳 slot）。
func allowSlotCode(ownerCode, spenderCode []byte) []byte {
	var b []byte
	b = append(b, ownerCode...)
	b = append(b, 0x60, 0x00, 0x52)                       // MSTORE(0, owner)
	b = append(b, 0x60, 0x03, 0x60, 0x20, 0x52)           // MSTORE(32, base=3)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)           // SHA3(0,64) → inner
	b = append(b, 0x60, 0x00, 0x52)                       // MSTORE(0, inner)
	b = append(b, spenderCode...)
	b = append(b, 0x60, 0x20, 0x52)                       // MSTORE(32, spender)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)           // SHA3(0,64) → slot
	return b
}

var (
	codeCaller     = []byte{0x33}             // CALLER
	codeCalldata4  = []byte{0x60, 0x04, 0x35} // CALLDATALOAD(4)
	codeCalldata24 = []byte{0x60, 0x24, 0x35} // CALLDATALOAD(36)
	codeCalldata68 = []byte{0x60, 0x44, 0x35} // CALLDATALOAD(68)
	codeLog3Tail   = []byte{0x60, 0x20, 0x60, 0x00, 0xA3} // LOG3 size=32 offset=0
	codeReturnTrue = []byte{0x60, 0x01}       // PUSH1 1
)

// erc20SegTotalSupply: totalSupply → SLOAD slot0 → RETURN(0,32)
func erc20SegTotalSupply() builtSeg {
	b := []byte{0x5B, 0x60, 0x00, 0x54}
	return builtSeg{code: append(b, RET32...)}
}

// erc20SegBalanceOf: balanceOf → CALLDATALOAD(4) SLOAD → RETURN(0,32)
func erc20SegBalanceOf() builtSeg {
	b := []byte{0x5B, 0x60, 0x04, 0x35, 0x54}
	return builtSeg{code: append(b, RET32...)}
}

// erc20SegName: name → SLOAD slot1 → RETURN(0,32)
func erc20SegName() builtSeg {
	b := []byte{0x5B, 0x60, 0x01, 0x54}
	return builtSeg{code: append(b, RET32...)}
}

// erc20SegSymbol: symbol → SLOAD slot2 → RETURN(0,32)
func erc20SegSymbol() builtSeg {
	b := []byte{0x5B, 0x60, 0x02, 0x54}
	return builtSeg{code: append(b, RET32...)}
}

// erc20SegDecimals: decimals → 回傳 18
func erc20SegDecimals() builtSeg {
	return builtSeg{code: []byte{0x5B, 0x60, 0x12, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3}}
}

// erc20SegTransfer: transfer(to, amount)——餘額檢查（bal<amt→REVERT）、
// 扣 caller、加 to、LOG3(Transfer, caller, to, amount)、回傳 1。
func erc20SegTransfer() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                       // JUMPDEST
	b = append(b, 0x60, 0x04, 0x35)           // to
	b = append(b, 0x33, 0x54)                 // bal[caller]
	b = append(b, 0x60, 0x24, 0x35)           // amt
	b = append(b, 0x10, 0x15)                 // LT ISZERO
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)     // JUMPI ok
	jumps = append(jumps, segJump{pos: okPos, target: okPos + 8})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	b = append(b, 0x5B)                       // ok: [to]
	b = append(b, 0x33, 0x54)                 // bal[caller]
	b = append(b, 0x60, 0x24, 0x35, 0x03)     // newFrom
	b = append(b, 0x33, 0x55)                 // SSTORE slot[caller]=newFrom
	b = append(b, 0x60, 0x04, 0x35, 0x54)     // bal[to]
	b = append(b, 0x60, 0x24, 0x35, 0x01)     // newTo
	b = append(b, 0x60, 0x04, 0x35, 0x55)     // SSTORE slot[to]=newTo
	// LOG3(Transfer, caller, to, amount)：topic1(topic0) 最早 push（EVM topics 順序）
	b = append(b, 0x60, 0x24, 0x35)           // amt（data）
	b = append(b, 0x60, 0x00, 0x52)           // mem[0]=amt
	b = append(b, push32(selNum(TopicTransfer))...)
	b = append(b, 0x33)                       // caller（topic2）
	b = append(b, 0x60, 0x04, 0x35)           // to（topic3）
	b = append(b, codeLog3Tail...)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b, jumps: jumps}
}

// erc20SegApprove: approve(spender, amount)——寫 allowance[caller][spender]、
// LOG3(Approval, caller, spender, amount)、回傳 1。
func erc20SegApprove() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, allowSlotCode(codeCaller, codeCalldata4)...)
	b = append(b, codeCalldata24...) // amount（calldata[36]）
	b = append(b, 0x90)              // SWAP1 → [slot, amount]
	b = append(b, 0x55)              // SSTORE slot=amount
	// LOG3(Approval, caller, spender, amount)：topic1(topic0) 最早 push
	b = append(b, codeCalldata24...) // amount（data）
	b = append(b, 0x60, 0x00, 0x52)  // mem[0]=amount
	b = append(b, push32(selNum(TopicApproval))...)
	b = append(b, 0x33)              // caller（topic2）
	b = append(b, codeCalldata4...)  // spender（topic3）
	b = append(b, codeLog3Tail...)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// erc20SegAllowance: allowance(owner, spender)——讀嵌套映射 slot → RETURN(0,32)
func erc20SegAllowance() builtSeg {
	b := []byte{0x5B} // JUMPDEST
	b = append(b, allowSlotCode(codeCalldata4, codeCalldata24)...)
	b = append(b, 0x54) // SLOAD
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// erc20SegTransferFrom: transferFrom(from, to, amount)——檢查 allowance[from][caller]
// ≥ amount 與 bal[from] ≥ amount，扣 allowance 與 from、加 to、
// LOG3(Transfer, from, to, amount)、回傳 1。
func erc20SegTransferFrom() builtSeg {
	var b []byte
	var jumps []segJump
	// 1) allowance 檢查
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, allowSlotCode(codeCalldata4, codeCaller)...)
	b = append(b, 0x80)            // DUP1（保留 slot）
	b = append(b, 0x54)            // allow
	b = append(b, codeCalldata68...) // amt
	b = append(b, 0x10, 0x15)      // LT ISZERO
	ok1Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)     // JUMPI ok1
	jumps = append(jumps, segJump{pos: ok1Pos, target: ok1Pos + 8})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	b = append(b, 0x5B)                       // ok1：重算 slot（不可信賴跳轉前的殘留值）
	b = append(b, allowSlotCode(codeCalldata4, codeCaller)...)
	b = append(b, 0x80)                       // DUP1（保留 slot 供 SSTORE）
	b = append(b, 0x54)                       // allow
	b = append(b, codeCalldata68...)          // amt
	b = append(b, 0x03)                       // newAllow
	b = append(b, 0x90)                       // SWAP1 → [slot, newAllow]（SUB 後新值在頂）
	b = append(b, 0x55)                       // SSTORE allowance=newAllow
	// 2) from 餘額檢查＋扣
	b = append(b, codeCalldata4...)           // from
	b = append(b, 0x54)                       // bal[from]
	b = append(b, codeCalldata68...)          // amt
	b = append(b, 0x10, 0x15)                 // LT ISZERO
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)     // JUMPI ok2
	jumps = append(jumps, segJump{pos: ok2Pos, target: ok2Pos + 8})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	b = append(b, 0x5B)                       // ok2
	b = append(b, codeCalldata4...)           // from
	b = append(b, 0x54)                       // bal[from]
	b = append(b, codeCalldata68...)          // amt
	b = append(b, 0x03)                       // newFrom
	b = append(b, codeCalldata4...)           // from
	b = append(b, 0x55)                       // SSTORE bal[from]=newFrom（from 已在棧頂）
	// 3) to 加款
	b = append(b, codeCalldata24...)          // to
	b = append(b, 0x54)                       // bal[to]
	b = append(b, codeCalldata68...)          // amt
	b = append(b, 0x01)                       // newTo
	b = append(b, codeCalldata24...)          // to
	b = append(b, 0x55)                       // SSTORE bal[to]=newTo（to 已在棧頂）
	// 4) LOG3(Transfer, from, to, amount)：topic1(topic0) 最早 push
	b = append(b, codeCalldata68...)          // amt（data）
	b = append(b, 0x60, 0x00, 0x52)           // mem[0]=amt
	b = append(b, push32(selNum(TopicTransfer))...)
	b = append(b, codeCalldata4...)           // from（topic2）
	b = append(b, codeCalldata24...)          // to（topic3）
	b = append(b, codeLog3Tail...)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b, jumps: jumps}
}

// Erc20Init 回傳部署位元組碼（無 name/symbol 元資料）：把 totalSupply 寫入
// slot0、並將總供應量全部結給部署者（CALLER=creator），最後回傳 runtime。
func Erc20Init(totalSupply *big.Int) []byte {
	return Erc20InitWithMeta(totalSupply, nil, nil)
}

// Erc20InitWithMeta 回傳帶元資料（name/symbol）的部署位元組碼：額外把
// name 寫入 slot1、symbol 寫入 slot2（皆為右對齊 32-byte）。
func Erc20InitWithMeta(totalSupply *big.Int, name, symbol []byte) []byte {
	runtime := Erc20Runtime()
	var b []byte
	// slot0 = totalSupply（SSTORE：key 棧頂，先壓 value 再壓 key 0）
	b = append(b, push32(totalSupply)...)
	b = append(b, 0x60, 0x00) // key 0
	b = append(b, 0x55)       // SSTORE
	// slot1 = name（右對齊）
	b = append(b, push32(new(big.Int).SetBytes(name))...)
	b = append(b, 0x60, 0x01) // key 1
	b = append(b, 0x55)       // SSTORE
	// slot2 = symbol（右對齊）
	b = append(b, push32(new(big.Int).SetBytes(symbol))...)
	b = append(b, 0x60, 0x02) // key 2
	b = append(b, 0x55)       // SSTORE
	// slot[creator] = totalSupply（key 棧頂：先壓 value=supply、再壓 key=creator）
	b = append(b, push32(totalSupply)...)
	b = append(b, 0x33) // CALLER → creator（key 頂）
	b = append(b, 0x55) // SSTORE
	// CODECOPY(runtime) + RETURN(runtime)：runtime 位於 init 指令段
	// （CODECOPY 段 9 bytes + RETURN 段 6 bytes，size/src 用 PUSH2）
	// 之後，src = initLen + 15。
	initLen := len(b)
	rl := len(runtime)
	src := initLen + 15
	b = append(b, 0x61, byte(rl>>8), byte(rl&0xFF))                   // PUSH2 size
	b = append(b, 0x61, byte(src>>8), byte(src&0xFF))                 // PUSH2 src
	b = append(b, 0x60, 0x00)                                         // PUSH dest
	b = append(b, 0x39)                                               // CODECOPY（dest 棧頂）
	b = append(b, 0x61, byte(rl>>8), byte(rl&0xFF), 0x60, 0x00, 0xF3) // RETURN(0, rl)
	return append(b, runtime...)
}

// Erc20BalanceOfCalldata 組裝 balanceOf(address) calldata。
func Erc20BalanceOfCalldata(holder0x string) []byte {
	out := append([]byte{}, SelectorBalanceOf...)
	return append(out, IntToBytes(addrToInt(holder0x), 32)...)
}

// Erc20TransferCalldata 組裝 transfer(to, amount) calldata。
func Erc20TransferCalldata(to0x string, amount *big.Int) []byte {
	out := append([]byte{}, SelectorTransfer...)
	out = append(out, IntToBytes(addrToInt(to0x), 32)...)
	return append(out, IntToBytes(amount, 32)...)
}

// Erc20TotalSupplyCalldata 組裝 totalSupply() calldata。
func Erc20TotalSupplyCalldata() []byte {
	return append([]byte{}, SelectorTotalSupply...)
}

// Erc20NameCalldata 組裝 name() calldata。
func Erc20NameCalldata() []byte {
	return append([]byte{}, SelectorName...)
}

// Erc20SymbolCalldata 組裝 symbol() calldata。
func Erc20SymbolCalldata() []byte {
	return append([]byte{}, SelectorSymbol...)
}

// Erc20ApproveCalldata 組裝 approve(spender, amount) calldata。
func Erc20ApproveCalldata(spender0x string, amount *big.Int) []byte {
	out := append([]byte{}, SelectorApprove...)
	out = append(out, IntToBytes(addrToInt(spender0x), 32)...)
	return append(out, IntToBytes(amount, 32)...)
}

// Erc20AllowanceCalldata 組裝 allowance(owner, spender) calldata。
func Erc20AllowanceCalldata(owner0x, spender0x string) []byte {
	out := append([]byte{}, SelectorAllowance...)
	out = append(out, IntToBytes(addrToInt(owner0x), 32)...)
	return append(out, IntToBytes(addrToInt(spender0x), 32)...)
}

// Erc20TransferFromCalldata 組裝 transferFrom(from, to, amount) calldata。
func Erc20TransferFromCalldata(from0x, to0x string, amount *big.Int) []byte {
	out := append([]byte{}, SelectorTransferFrom...)
	out = append(out, IntToBytes(addrToInt(from0x), 32)...)
	out = append(out, IntToBytes(addrToInt(to0x), 32)...)
	return append(out, IntToBytes(amount, 32)...)
}

// Erc20DecimalsCalldata 組裝 decimals() calldata。
func Erc20DecimalsCalldata() []byte {
	return append([]byte{}, SelectorDecimals...)
}
