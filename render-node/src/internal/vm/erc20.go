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
	// EIP-2612 permit（EIP-712 離線授權）selector。
	SelectorPermit          = []byte{0xd5, 0x05, 0xac, 0xcf} // permit(address,address,uint256,uint256,uint8,bytes32,bytes32)
	SelectorNonces          = []byte{0x7e, 0xce, 0xbe, 0x00} // nonces(address)
	SelectorDomainSeparator = []byte{0x36, 0x44, 0xe5, 0x15} // DOMAIN_SEPARATOR()
	// 標準事件 topic0（keccak256 簽名）——與乙太坊主網一致。
	TopicTransfer = []byte{0xdd, 0xf2, 0x52, 0xad, 0x1b, 0xe2, 0xc8, 0x9b,
		0x69, 0xc2, 0xb0, 0x68, 0xfc, 0x37, 0x8d, 0xaa, 0x95, 0x2b, 0xa7, 0xf1,
		0x63, 0xc4, 0xa1, 0x16, 0x28, 0xf5, 0x5a, 0x4d, 0xf5, 0x23, 0xb3, 0xef}
	TopicApproval = []byte{0x8c, 0x5b, 0xe1, 0xe5, 0xeb, 0xec, 0x7d, 0x5b,
		0xd1, 0x4f, 0x71, 0x42, 0x7d, 0x1e, 0x84, 0xf3, 0xdd, 0x03, 0x14, 0xc0,
		0xf7, 0xb2, 0x29, 0x1e, 0x5b, 0x20, 0x0a, 0xc8, 0xc7, 0xc3, 0xb9, 0x25}
	// allowance 嵌套映射基礎 slot（自訂布局：keccak(spender‖keccak(owner‖3))）。
	AllowanceBaseSlot = big.NewInt(3)
	// EIP-2612 permit 常數（EIP-712）。
	// PermitTypeHash：keccak256("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)")
	PermitTypeHash = BytesToInt(Keccak256([]byte("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)")))
	// DomainTypeHash：keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	DomainTypeHash = BytesToInt(Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")))
	// VersionHash：keccak256("1")——permit domain 固定 version。
	VersionHash = BytesToInt(Keccak256([]byte("1")))
	// NonceBaseSlot：nonce 映射基礎 slot（mapping(address=>uint256) at slot 4，與 allowance/balance 錯開）。
	NonceBaseSlot = big.NewInt(4)
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
//	nonces(owner)        = keccak(owner ‖ 4)（EIP-2612 mapping，slot 4）
func Erc20Runtime() []byte {
	return erc20RuntimeBuild(nil)
}

// Erc20RuntimePermit 回傳含 EIP-2612 permit / nonces / DOMAIN_SEPARATOR 的
// runtime 位元組碼。nameHash＝keccak256(name)（EIP-712 domain name hash，
// 由部署流程以實際 name 計算後注入）。
func Erc20RuntimePermit(nameHash *big.Int) []byte {
	if nameHash == nil {
		nameHash = BytesToInt(Keccak256(nil))
	}
	return erc20RuntimeBuild(nameHash)
}

// erc20RuntimeBuild 為 runtime 共用建構：nameHash 為 nil 時只有 9 個標準
// selector（無 permit）；非 nil 時追加 nonces/DOMAIN_SEPARATOR/permit。
func erc20RuntimeBuild(nameHash *big.Int) []byte {
	// ---- dispatch：9 個（無 permit）或 12 個（含 permit）selector 比較對＋fallback ----
	selOrder := [][]byte{
		SelectorTotalSupply, SelectorBalanceOf, SelectorTransfer,
		SelectorName, SelectorSymbol,
		SelectorApprove, SelectorAllowance, SelectorTransferFrom, SelectorDecimals,
	}
	segs := []builtSeg{
		erc20SegTotalSupply(), erc20SegBalanceOf(), erc20SegTransfer(),
		erc20SegName(), erc20SegSymbol(), erc20SegApprove(), erc20SegAllowance(),
		erc20SegTransferFrom(), erc20SegDecimals(),
	}
	if nameHash != nil {
		selOrder = append(selOrder, SelectorNonces, SelectorDomainSeparator, SelectorPermit)
		segs = append(segs, erc20SegNonces(), erc20SegDomainSeparator(nameHash), erc20SegPermit(nameHash))
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
	codeCaller       = []byte{0x33}             // CALLER
	codeCalldata4    = []byte{0x60, 0x04, 0x35} // CALLDATALOAD(4)
	codeCalldata24   = []byte{0x60, 0x24, 0x35} // CALLDATALOAD(36)
	codeCalldata36   = []byte{0x60, 0x24, 0x35} // CALLDATALOAD(36)（permit spender）
	codeCalldata68   = []byte{0x60, 0x44, 0x35} // CALLDATALOAD(68)
	codePermitDeadline  = []byte{0x60, 0x64, 0x35} // CALLDATALOAD(100)（permit deadline）
	codePermitV  = []byte{0x60, 0x84, 0x35} // CALLDATALOAD(132)（permit v）
	codePermitR  = []byte{0x60, 0xA4, 0x35} // CALLDATALOAD(164)（permit r）
	codePermitS  = []byte{0x60, 0xC4, 0x35} // CALLDATALOAD(196)（permit s）
	codeLog3Tail     = []byte{0x60, 0x20, 0x60, 0x00, 0xA3} // LOG3 size=32 offset=0
	codeReturnTrue   = []byte{0x60, 0x01}       // PUSH1 1
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

// erc20SegNonces: nonces(owner)——SLOAD keccak(owner‖4) → RETURN(0,32)。
// nonce slot 布局：mapping(address=>uint256) at slot 4（與 allowance/balance 錯開）。
func erc20SegNonces() builtSeg {
	b := []byte{0x5B} // JUMPDEST
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x00, 0x52)            // mem[0]=owner
	b = append(b, 0x60, 0x04, 0x60, 0x20, 0x52) // mem[32]=4
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20) // SHA3(0,64) → slot
	b = append(b, 0x54)                        // SLOAD
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// erc20SegDomainSeparator: DOMAIN_SEPARATOR()——keccak256(DOMAIN_TYPEHASH‖
// nameHash‖versionHash‖chainId‖this) → RETURN(0,32)。chainId 取 CHAINID
// opcode（0x46）、verifyingContract 取 ADDRESS（address(this)），與
// EIP-712 標準一致。
func erc20SegDomainSeparator(nameHash *big.Int) builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, push32(DomainTypeHash)...)
	b = append(b, 0x60, 0x00, 0x52)            // mem[0]=typehash
	b = append(b, push32(nameHash)...)
	b = append(b, 0x60, 0x20, 0x52)            // mem[32]=nameHash
	b = append(b, push32(VersionHash)...)
	b = append(b, 0x60, 0x40, 0x52)            // mem[64]=versionHash
	b = append(b, 0x46)                        // CHAINID
	b = append(b, 0x60, 0x60, 0x52)            // mem[96]=chainId
	b = append(b, 0x30)                        // ADDRESS（address(this)）
	b = append(b, 0x60, 0x80, 0x52)            // mem[128]=verifyingContract
	b = append(b, 0x60, 0xA0, 0x60, 0x00, 0x20) // SHA3(0,160) → domain
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// erc20SegPermit: permit(owner, spender, value, deadline, v, r, s)——EIP-2612
// 離線授權。流程：
//  1. deadline 檢查：now > deadline → REVERT。
//  2. nonce = SLOAD keccak(owner‖4)。
//  3. structHash = keccak256(PERMIT_TYPEHASH‖owner‖spender‖value‖nonce‖deadline)。
//  4. domainSeparator = keccak256(DOMAIN_TYPEHASH‖nameHash‖versionHash‖chainId‖this)。
//  5. digest = keccak256("\x19\x01"‖domainSeparator‖structHash)。
//  6. ecrecover（precompile 0x01）→ recovered == owner，否則 REVERT。
//  7. nonce+1 SSTORE；allowance[owner][spender] = value SSTORE。
//  8. 回傳 1。
func erc20SegPermit(nameHash *big.Int) builtSeg {
	var b []byte
	var jumps []segJump

	b = append(b, 0x5B) // JUMPDEST

	// ---- 1) deadline 檢查：now > deadline → REVERT ----
	b = append(b, 0x42)                    // TIMESTAMP → now
	b = append(b, codePermitDeadline...)      // deadline
	b = append(b, 0x11, 0x15)              // GT ISZERO（有效時=1）
	deadlineJump := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI ok_deadline
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	okDeadline := len(b)
	b = append(b, 0x5B)                    // ok_deadline
	jumps = append(jumps, segJump{pos: deadlineJump, target: okDeadline})

	// ---- 2) nonce = SLOAD keccak(owner‖4) ----
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x00, 0x52)            // mem[0]=owner
	b = append(b, 0x60, 0x04, 0x60, 0x20, 0x52) // mem[32]=4
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20) // SHA3(0,64) → slot
	b = append(b, 0x54)                        // SLOAD → nonce

	// ---- 3) structHash = keccak256(PERMIT_TYPEHASH‖owner‖spender‖value‖nonce‖deadline) ----
	b = append(b, push32(PermitTypeHash)...)
	b = append(b, 0x60, 0x00, 0x52)            // mem[0]=typehash
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x20, 0x52)            // mem[32]=owner
	b = append(b, codeCalldata36...)
	b = append(b, 0x60, 0x40, 0x52)            // mem[64]=spender
	b = append(b, codeCalldata68...)
	b = append(b, 0x60, 0x60, 0x52)            // mem[96]=value
	b = append(b, 0x80)                        // DUP1 nonce
	b = append(b, 0x60, 0x80, 0x52)            // mem[128]=nonce
	b = append(b, codePermitDeadline...)
	b = append(b, 0x60, 0xA0, 0x52)            // mem[160]=deadline
	b = append(b, 0x60, 0xC0, 0x60, 0x00, 0x20) // SHA3(0,192) → structHash

	// ---- 4) domainSeparator = keccak256(DOMAIN_TYPEHASH‖nameHash‖versionHash‖chainId‖this) ----
	b = append(b, push32(DomainTypeHash)...)
	b = append(b, 0x60, 0x00, 0x52)
	b = append(b, push32(nameHash)...)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, push32(VersionHash)...)
	b = append(b, 0x60, 0x40, 0x52)
	b = append(b, 0x46)                        // CHAINID
	b = append(b, 0x60, 0x60, 0x52)
	b = append(b, 0x30)                        // ADDRESS（address(this)）
	b = append(b, 0x60, 0x80, 0x52)            // mem[128]=verifyingContract
	b = append(b, 0x60, 0xA0, 0x60, 0x00, 0x20) // SHA3(0,160) → domain

	// ---- 5) digest = keccak256("\x19\x01"‖domain‖structHash)；寫回 mem[0:32] ----
	b = append(b, 0x60, 0x19, 0x60, 0x00, 0x53) // MSTORE8(0,0x19)
	b = append(b, 0x60, 0x01, 0x60, 0x01, 0x53) // MSTORE8(1,0x01)
	b = append(b, 0x80, 0x60, 0x02, 0x52)       // DUP1 domain；MSTORE(2,domain)
	b = append(b, 0x81, 0x60, 0x22, 0x52)       // DUP2 structHash；MSTORE(34,structHash)
	b = append(b, 0x60, 0x42, 0x60, 0x00, 0x20) // SHA3(0,66) → digest（0x19 0x01‖domain‖structHash）
	b = append(b, 0x60, 0x00, 0x52)             // mem[0]=digest（ecrecover input）

	// ---- 6) ecrecover：CALL precompile 0x01，input=digest‖v‖r‖s（mem[0:128]）----
	b = append(b, codePermitV...)
	b = append(b, 0x60, 0x20, 0x52)            // mem[32]=v
	b = append(b, codePermitR...)
	b = append(b, 0x60, 0x40, 0x52)            // mem[64]=r
	b = append(b, codePermitS...)
	b = append(b, 0x60, 0x60, 0x52)            // mem[96]=s
	b = append(b, 0x60, 0x20)                  // outSize=32（棧底）
	b = append(b, 0x60, 0x00)                  // outOffset=0
	b = append(b, 0x61, 0x00, 0x80)            // inSize=128
	b = append(b, 0x60, 0x00)                  // inOffset=0
	b = append(b, 0x60, 0x00)                  // value=0
	b = append(b, 0x60, 0x01)                  // addr=0x01
	b = append(b, 0x61, 0x0B, 0xB8)            // gas=3000（棧頂，EVM 慣例）
	b = append(b, 0xF1)                        // CALL → ok
	b = append(b, 0x15)                        // ISZERO（ok=0 時=1）
	callFailJump := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)      // JUMPI revert_label
	b = append(b, 0x60, 0x00, 0x51)            // MLOAD(0) → recovered
	b = append(b, codeCalldata4...)            // owner
	b = append(b, 0x14)                        // EQ（相等=1）
	okSignJump := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)      // JUMPI ok_sign
	revertLabel := len(b)
	b = append(b, 0x5B)                        // JUMPDEST revert_label
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	jumps = append(jumps, segJump{pos: callFailJump, target: revertLabel})
	okSign := len(b)
	b = append(b, 0x5B)                        // JUMPDEST ok_sign
	jumps = append(jumps, segJump{pos: okSignJump, target: okSign})

	// ---- 7) nonce+1：重算 slot → SLOAD → +1 → SSTORE ----
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x04, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20) // SHA3 → slot
	b = append(b, 0x80)                        // DUP1
	b = append(b, 0x54)                        // SLOAD → nonce
	b = append(b, 0x60, 0x01)                  // PUSH1 1
	b = append(b, 0x01)                        // ADD
	b = append(b, 0x90)                        // SWAP1 → [nonce+1, slot]
	b = append(b, 0x55)                        // SSTORE

	// ---- 8) allowance[owner][spender] = value ----
	b = append(b, allowSlotCode(codeCalldata4, codeCalldata36)...)
	b = append(b, codeCalldata68...)            // value
	b = append(b, 0x90)                        // SWAP1 → [value, slot]
	b = append(b, 0x55)                        // SSTORE

	// ---- 9) 回傳 1 ----
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)

	return builtSeg{code: b, jumps: jumps}
}

// Erc20Init 回傳部署位元組碼（無 name/symbol 元資料，不含 permit）：把
// totalSupply 寫入 slot0、並將總供應量全部結給部署者（CALLER=creator），
// 最後回傳 runtime。
func Erc20Init(totalSupply *big.Int) []byte {
	runtime := Erc20Runtime()
	var b []byte
	b = append(b, push32(totalSupply)...)
	b = append(b, 0x60, 0x00) // key 0
	b = append(b, 0x55)       // SSTORE
	b = append(b, push32(totalSupply)...)
	b = append(b, 0x33) // CALLER → creator（key 頂）
	b = append(b, 0x55) // SSTORE
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

// Erc20InitWithMeta 回傳帶元資料（name/symbol）的部署位元組碼：額外把
// name 寫入 slot1、symbol 寫入 slot2（皆為右對齊 32-byte），並採用含
// EIP-2612 permit 的 runtime（新部署合約一律支援 permit）。
func Erc20InitWithMeta(totalSupply *big.Int, name, symbol []byte) []byte {
	runtime := Erc20RuntimePermit(BytesToInt(Keccak256(name)))
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

// Erc20PermitCalldata 組裝 permit(owner, spender, value, deadline, v, r, s) calldata。
func Erc20PermitCalldata(owner0x, spender0x string, value, deadline *big.Int, v byte, r, s []byte) []byte {
	out := append([]byte{}, SelectorPermit...)
	out = append(out, IntToBytes(addrToInt(owner0x), 32)...)
	out = append(out, IntToBytes(addrToInt(spender0x), 32)...)
	out = append(out, IntToBytes(value, 32)...)
	out = append(out, IntToBytes(deadline, 32)...)
	out = append(out, IntToBytes(big.NewInt(int64(v)), 32)...)
	out = append(out, IntToBytes(new(big.Int).SetBytes(r), 32)...)
	return append(out, IntToBytes(new(big.Int).SetBytes(s), 32)...)
}

// Erc20NoncesCalldata 組裝 nonces(owner) calldata。
func Erc20NoncesCalldata(owner0x string) []byte {
	out := append([]byte{}, SelectorNonces...)
	return append(out, IntToBytes(addrToInt(owner0x), 32)...)
}

// Erc20DomainSeparatorCalldata 組裝 DOMAIN_SEPARATOR() calldata。
func Erc20DomainSeparatorCalldata() []byte {
	return append([]byte{}, SelectorDomainSeparator...)
}
