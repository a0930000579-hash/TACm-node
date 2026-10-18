package vm

import (
	"math/big"
)

// TAC-ENS 鏈上命名服務（P1-①）：讓帳戶可用「name.tac」名稱代替長地址，並支援
// 名稱→地址解析與地址→名稱反向解析，與主流鏈（以太坊 ENS / BSC）身份層同級。
//
// Slot 佈局（固定，與 ERC-20/721 的部署者 slot10 約定一致）：
//
//	slot1 = mapping(labelhash → owner)        // 名稱持有人
//	slot2 = mapping(labelhash → expiry)       // 到期時間（unix 秒，0=未註冊）
//	slot3 = mapping(labelhash → addr)         // 名稱解析到的地址（0x…）
//	slot4 = mapping(labelhash → label 原值)   // 原始 label（≤31 bytes 右補零，顯示用）
//	slot5 = mapping(address → labelhash)      // 反向解析（一個地址一個名稱）
//	slot10 = 部署者（管理員，保留）
//
// 註冊規則（對齊 ENS 精神）：任一帳戶可註冊未使用（owner==0 或已過期）的 label；
// 名稱過期後可被他人重新註冊；持有人可 setAddr/transfer；任何人可 setReverse。
// 付費與競價機制於後續模組接入（商業價值階段）。

var (
	SelectorNSRegister          = []byte{0x85, 0x8e, 0x96, 0x31} // register(bytes32,uint64)
	SelectorNSRegisterTo        = []byte{0xaa, 0xf2, 0x15, 0x66} // registerTo(bytes32,address,uint64)（代註冊，指定 owner）
	SelectorNSRenew             = []byte{0x3a, 0x83, 0xcf, 0xef} // renew(bytes32,uint64)
	SelectorNSSetAddr           = []byte{0xd5, 0xfa, 0x2b, 0x00} // setAddr(bytes32,address)
	SelectorNSAddr              = []byte{0x3b, 0x3b, 0x57, 0xde} // addr(bytes32)
	SelectorNSOwner             = []byte{0x02, 0x57, 0x1b, 0xe3} // owner(bytes32)
	SelectorNSTransfer          = []byte{0x79, 0xce, 0x9f, 0xac} // transfer(bytes32,address)
	SelectorNSSetReverse        = []byte{0xb8, 0x41, 0xf4, 0x4d} // setReverse(bytes32)
	SelectorNSReverse           = []byte{0xe3, 0x0b, 0xd7, 0x40} // reverse(address)
	SelectorNSResolve           = []byte{0x5c, 0x23, 0xbd, 0xf5} // resolve(bytes32)
	SelectorNSExpiry            = []byte{0xd2, 0xec, 0x5f, 0xca} // expiry(bytes32)
	SelectorNSNameExists        = []byte{0x14, 0x3d, 0xc3, 0xf8} // nameExists(bytes32)
	SelectorNSSupportsInterface = []byte{0x01, 0xff, 0xc9, 0xa7} // supportsInterface(bytes4)
)

// TACNamesRuntime 回傳 TAC-ENS 註冊表 runtime 位元組碼（固定）。
// selector 解析與 Erc20/Erc721Runtime 相同（CALLDATALOAD(0)→DIV 2^224→DUP1→EQ→JUMPI）。
func TACNamesRuntime() []byte {
	selOrder := [][]byte{
		SelectorNSRegister, SelectorNSRegisterTo, SelectorNSRenew, SelectorNSSetAddr,
		SelectorNSAddr, SelectorNSOwner, SelectorNSTransfer,
		SelectorNSSetReverse, SelectorNSReverse, SelectorNSResolve,
		SelectorNSExpiry, SelectorNSNameExists, SelectorNSSupportsInterface,
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
		tacNamesSegRegister(), tacNamesSegRegisterTo(), tacNamesSegRenew(), tacNamesSegSetAddr(),
		tacNamesSegAddr(), tacNamesSegOwner(), tacNamesSegTransfer(),
		tacNamesSegSetReverse(), tacNamesSegReverse(), tacNamesSegResolve(),
		tacNamesSegExpiry(), tacNamesSegNameExists(), tacNamesSegSupportsInterface(),
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

// TACNamesInit 產生部署 init：記錄部署者到 slot10，CODECOPY runtime 並 RETURN。
func TACNamesInit() []byte {
	runtime := TACNamesRuntime()
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

// tacNamesSegRegister: register(bytes32,uint64)——label 可用（owner==0 或 expiry<ts）
// 時註冊：owner=caller、expiry=ts+years、slot4 存 label 原值、reverse[caller]=label。
func tacNamesSegRegister() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, 0x80)                               // [slot1, slot1]
	b = append(b, 0x54)                               // [slot1, owner]
	b = append(b, 0x15)                               // [slot1, iszero]
	ok1Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: ok1Pos, target: 0})
	b = append(b, 0x50)                               // POP slot1
	b = append(b, mapSlotCode(codeCalldata4, 2)...)  // [slot2]
	b = append(b, 0x54)                               // [expiry]
	b = append(b, 0x42)                               // TIMESTAMP → [expiry, ts]
	b = append(b, 0x10)                               // LT → [expiry<ts]
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: ok2Pos, target: 0})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（名稱使用中）
	b = append(b, 0x5B)                               // ok:
	// owner=caller
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, codeCaller...)                       // [slot1, caller]
	b = append(b, 0x90)                               // SWAP1 → [caller, slot1]
	b = append(b, 0x55)                               // SSTORE owner=caller
	// expiry = ts + years
	b = append(b, mapSlotCode(codeCalldata4, 2)...)  // [slot2]
	b = append(b, 0x42)                               // [slot2, ts]
	b = append(b, codeCalldata24...)                   // [slot2, ts, years]
	b = append(b, 0x01)                               // ADD → [slot2, ts+years]
	b = append(b, 0x90)                               // SWAP1 → [ts+years, slot2]
	b = append(b, 0x55)                               // SSTORE expiry=ts+years
	// slot4 存 label 原值（顯示用）
	b = append(b, mapSlotCode(codeCalldata4, 4)...)  // [slot4]
	b = append(b, codeCalldata4...)                    // [slot4, label]
	b = append(b, 0x90)                               // SWAP1 → [label, slot4]
	b = append(b, 0x55)                               // SSTORE label[hash]=label
	// reverse[caller]=label
	b = append(b, codeCaller...)                       // [caller]
	b = append(b, mapSlotCode(codeCaller, 5)...)      // [caller, slot5]
	b = append(b, codeCalldata4...)                    // [caller, slot5, label]
	b = append(b, 0x90)                               // SWAP1 → [caller, label, slot5]
	b = append(b, 0x55)                               // SSTORE reverse[caller]=label
	b = append(b, 0x50)                               // POP（清殘留 caller）
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	for i := range jumps {
		jumps[i].target = ok1Pos + 4 // ok 的 JUMPDEST 位於 REVERT 之後；用「ok1Pos+8」
	}
	// 修正：ok 實際位置 = REVERT(5 bytes) 之後，即 ok1Pos+4+5+1 之前。改用掃描定位。
	okIdx := indexOfByte(b, 0x5B, ok1Pos+4)
	for i := range jumps {
		jumps[i].target = okIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// indexOfByte 自 off 起找第一個 b 的索引。
func indexOfByte(bs []byte, want byte, off int) int {
	for i := off; i < len(bs); i++ {
		if bs[i] == want {
			return i
		}
	}
	return -1
}

// tacNamesSegRegisterTo: registerTo(bytes32,address,uint64)——代註冊：label 可用時
// 以「指定 to」為 owner（主流 ENS registerWithConfig 精神，供節點/服務代用戶註冊）。
func tacNamesSegRegisterTo() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, 0x80)                               // [slot1, slot1]
	b = append(b, 0x54)                               // [slot1, owner]
	b = append(b, 0x15)                               // [slot1, iszero]
	ok1Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: ok1Pos, target: 0})
	b = append(b, 0x50)                               // POP slot1
	b = append(b, mapSlotCode(codeCalldata4, 2)...)  // [slot2]
	b = append(b, 0x54)                               // [expiry]
	b = append(b, 0x42)                               // [expiry, ts]
	b = append(b, 0x10)                               // [expiry<ts]
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: ok2Pos, target: 0})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT
	b = append(b, 0x5B)                               // ok:
	// owner=to
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, codeCalldata24...)                   // [slot1, to]
	b = append(b, 0x90)                               // SWAP1
	b = append(b, 0x55)                               // SSTORE owner=to
	// expiry = ts + years（years=calldata[68:100]）
	b = append(b, mapSlotCode(codeCalldata4, 2)...)  // [slot2]
	b = append(b, 0x42)                               // [slot2, ts]
	b = append(b, codeCalldata68...)                   // [slot2, ts, years]
	b = append(b, 0x01)                               // ADD
	b = append(b, 0x90)                               // SWAP1
	b = append(b, 0x55)                               // SSTORE expiry
	// slot4 存 label 原值
	b = append(b, mapSlotCode(codeCalldata4, 4)...)  // [slot4]
	b = append(b, codeCalldata4...)                    // [slot4, label]
	b = append(b, 0x90)                               // SWAP1
	b = append(b, 0x55)                               // SSTORE label
	// reverse[to]=label
	b = append(b, codeCalldata24...)                   // [to]
	b = append(b, mapSlotCode(codeCalldata24, 5)...) // [to, slot5]
	b = append(b, codeCalldata4...)                    // [to, slot5, label]
	b = append(b, 0x90)                               // SWAP1
	b = append(b, 0x55)                               // SSTORE reverse[to]=label
	b = append(b, 0x50)                               // POP（清殘留 to）
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	okIdx := indexOfByte(b, 0x5B, ok1Pos+4)
	for i := range jumps {
		jumps[i].target = okIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacNamesSegRenew: renew(bytes32,uint64)——已註冊（expiry>0）名稱續期 expiry+=years。
func tacNamesSegRenew() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, mapSlotCode(codeCalldata4, 2)...)  // [slot2]
	b = append(b, 0x80)                               // [slot2, slot2]
	b = append(b, 0x54)                               // [slot2, expiry]
	b = append(b, 0x15)                               // [slot2, iszero]
	rvPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI rv
	jumps = append(jumps, segJump{pos: rvPos, target: 0})
	b = append(b, 0x80)                               // [slot2, slot2]
	b = append(b, 0x54)                               // [slot2, expiry]
	b = append(b, codeCalldata24...)                   // [slot2, expiry, years]
	b = append(b, 0x01)                               // ADD → [slot2, expiry+years]
	b = append(b, 0x90)                               // SWAP1 → [expiry+years, slot2]
	b = append(b, 0x55)                               // SSTORE expiry+=years
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	b = append(b, 0x5B)                               // rv:
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（未註冊）
	rvIdx := indexOfByte(b, 0x5B, rvPos+4)
	for i := range jumps {
		jumps[i].target = rvIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacNamesSegSetAddr: setAddr(bytes32,address)——僅 owner 可設解析地址（slot3）。
func tacNamesSegSetAddr() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, 0x80)                               // [slot1, slot1]
	b = append(b, 0x54)                               // [slot1, owner]
	b = append(b, codeCaller...)                       // [slot1, owner, caller]
	b = append(b, 0x14)                               // EQ → [slot1, eq]
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: okPos, target: 0})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（非 owner）
	b = append(b, 0x5B)                               // ok:
	b = append(b, 0x50)                               // POP slot1
	b = append(b, mapSlotCode(codeCalldata4, 3)...)  // [slot3]
	b = append(b, codeCalldata24...)                   // [slot3, to]
	b = append(b, 0x90)                               // SWAP1 → [to, slot3]
	b = append(b, 0x55)                               // SSTORE addr=to
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	okIdx := indexOfByte(b, 0x5B, okPos+4)
	for i := range jumps {
		jumps[i].target = okIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacNamesSegTransfer: transfer(bytes32,address)——僅 owner 可轉移名稱所有權。
func tacNamesSegTransfer() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, 0x80)                               // [slot1, slot1]
	b = append(b, 0x54)                               // [slot1, owner]
	b = append(b, codeCaller...)                       // [slot1, owner, caller]
	b = append(b, 0x14)                               // EQ → [slot1, eq]
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI ok
	jumps = append(jumps, segJump{pos: okPos, target: 0})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)       // REVERT（非 owner）
	b = append(b, 0x5B)                               // ok:
	b = append(b, 0x50)                               // POP slot1
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, codeCalldata24...)                   // [slot1, to]
	b = append(b, 0x90)                               // SWAP1 → [to, slot1]
	b = append(b, 0x55)                               // SSTORE owner=to
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	okIdx := indexOfByte(b, 0x5B, okPos+4)
	for i := range jumps {
		jumps[i].target = okIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacNamesSegSetReverse: setReverse(bytes32)——任何人把 caller 的反向解析設為 label。
func tacNamesSegSetReverse() builtSeg {
	var b []byte
	b = append(b, 0x5B)                              // JUMPDEST
	b = append(b, codeCaller...)                      // [caller]
	b = append(b, mapSlotCode(codeCaller, 5)...)     // [caller, slot5]
	b = append(b, codeCalldata4...)                    // [caller, slot5, label]
	b = append(b, 0x90)                              // SWAP1 → [caller, label, slot5]
	b = append(b, 0x55)                              // SSTORE reverse[caller]=label
	b = append(b, 0x50)                              // POP（清殘留 caller）
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacNamesSegOwner: owner(bytes32) → slot1 值。
func tacNamesSegOwner() builtSeg {
	b := []byte{0x5B}
	b = append(b, mapSlotCode(codeCalldata4, 1)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

// tacNamesSegExpiry: expiry(bytes32) → slot2 值。
func tacNamesSegExpiry() builtSeg {
	b := []byte{0x5B}
	b = append(b, mapSlotCode(codeCalldata4, 2)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

// tacNamesSegAddr / tacNamesSegResolve: addr/resolve(bytes32) → slot3 值。
func tacNamesSegAddr() builtSeg {
	b := []byte{0x5B}
	b = append(b, mapSlotCode(codeCalldata4, 3)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

func tacNamesSegResolve() builtSeg {
	return tacNamesSegAddr()
}

// tacNamesSegReverse: reverse(address) → slot5 值（address→labelhash）。
func tacNamesSegReverse() builtSeg {
	b := []byte{0x5B}
	b = append(b, mapSlotCode(codeCalldata4, 5)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

// tacNamesSegNameExists: nameExists(bytes32) → 1（已註冊且未過期）／0。
func tacNamesSegNameExists() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)                               // JUMPDEST
	b = append(b, mapSlotCode(codeCalldata4, 1)...)  // [slot1]
	b = append(b, 0x54)                               // [owner]
	b = append(b, 0x15)                               // [iszero]
	no1Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI no
	jumps = append(jumps, segJump{pos: no1Pos, target: 0})
	b = append(b, mapSlotCode(codeCalldata4, 2)...)  // [slot2]
	b = append(b, 0x54)                               // [expiry]
	b = append(b, 0x42)                               // [expiry, ts]
	b = append(b, 0x10)                               // [lt]
	no2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)             // JUMPI no
	jumps = append(jumps, segJump{pos: no2Pos, target: 0})
	b = append(b, codeReturnTrue...)                   // 有效 → 1
	b = append(b, RET32...)
	b = append(b, 0x5B)                               // no:
	b = append(b, 0x60, 0x00)                         // PUSH1 0（value）
	b = append(b, RET32...)                           // RETURN(0,32)=0
	noIdx := indexOfByte(b, 0x5B, no1Pos+4)
	for i := range jumps {
		jumps[i].target = noIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacNamesSegSupportsInterface: supportsInterface(bytes4) → 1 若 id==ERC-165。
func tacNamesSegSupportsInterface() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)             // JUMPDEST
	b = append(b, codeCalldata4...) // iface
	b = append(b, 0x80)
	b = append(b, push32(selNum(Interface165))...)
	b = append(b, 0x14)
	p := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI ok
	jumps = append(jumps, segJump{pos: p, target: 0})
	b = append(b, 0x50)       // POP（清 iface）
	b = append(b, 0x60, 0x00) // value 0
	b = append(b, RET32...)
	okIdx := len(b)
	b = append(b, 0x5B)       // ok
	b = append(b, 0x50)       // POP
	b = append(b, 0x60, 0x01) // value 1
	b = append(b, RET32...)
	for i := range jumps {
		jumps[i].target = okIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// ---- Calldata 組裝輔助（RPC 層使用）----

// TACNamesRegisterCalldata 組裝 register(bytes32,uint64) calldata。
// labelHash 為 keccak256(label)（32 bytes）。
func TACNamesRegisterCalldata(labelHash []byte, years *big.Int) []byte {
	out := append([]byte{}, SelectorNSRegister...)
	out = append(out, IntToBytes(BytesToInt(labelHash), 32)...)
	return append(out, IntToBytes(years, 32)...)
}

// TACNamesRegisterToCalldata 組裝 registerTo(bytes32,address,uint64) calldata
//（代註冊：以 to 為名稱 owner）。
func TACNamesRegisterToCalldata(labelHash []byte, to0x string, years *big.Int) []byte {
	out := append([]byte{}, SelectorNSRegisterTo...)
	out = append(out, IntToBytes(BytesToInt(labelHash), 32)...)
	out = append(out, IntToBytes(addrToInt(to0x), 32)...)
	return append(out, IntToBytes(years, 32)...)
}

// TACNamesRenewCalldata 組裝 renew(bytes32,uint64) calldata。
func TACNamesRenewCalldata(labelHash []byte, years *big.Int) []byte {
	out := append([]byte{}, SelectorNSRenew...)
	out = append(out, IntToBytes(BytesToInt(labelHash), 32)...)
	return append(out, IntToBytes(years, 32)...)
}

// TACNamesSetAddrCalldata 組裝 setAddr(bytes32,address) calldata。
func TACNamesSetAddrCalldata(labelHash []byte, to0x string) []byte {
	out := append([]byte{}, SelectorNSSetAddr...)
	out = append(out, IntToBytes(BytesToInt(labelHash), 32)...)
	return append(out, IntToBytes(addrToInt(to0x), 32)...)
}

// TACNamesAddrCalldata 組裝 addr(bytes32) calldata。
func TACNamesAddrCalldata(labelHash []byte) []byte {
	out := append([]byte{}, SelectorNSAddr...)
	return append(out, IntToBytes(BytesToInt(labelHash), 32)...)
}

// TACNamesOwnerCalldata 組裝 owner(bytes32) calldata。
func TACNamesOwnerCalldata(labelHash []byte) []byte {
	out := append([]byte{}, SelectorNSOwner...)
	return append(out, IntToBytes(BytesToInt(labelHash), 32)...)
}

// TACNamesExpiryCalldata 組裝 expiry(bytes32) calldata。
func TACNamesExpiryCalldata(labelHash []byte) []byte {
	out := append([]byte{}, SelectorNSExpiry...)
	return append(out, IntToBytes(BytesToInt(labelHash), 32)...)
}

// TACNamesResolveCalldata 組裝 resolve(bytes32) calldata。
func TACNamesResolveCalldata(labelHash []byte) []byte {
	out := append([]byte{}, SelectorNSResolve...)
	return append(out, IntToBytes(BytesToInt(labelHash), 32)...)
}

// TACNamesTransferCalldata 組裝 transfer(bytes32,address) calldata。
func TACNamesTransferCalldata(labelHash []byte, to0x string) []byte {
	out := append([]byte{}, SelectorNSTransfer...)
	out = append(out, IntToBytes(BytesToInt(labelHash), 32)...)
	return append(out, IntToBytes(addrToInt(to0x), 32)...)
}

// TACNamesSetReverseCalldata 組裝 setReverse(bytes32) calldata。
func TACNamesSetReverseCalldata(labelHash []byte) []byte {
	out := append([]byte{}, SelectorNSSetReverse...)
	return append(out, IntToBytes(BytesToInt(labelHash), 32)...)
}

// TACNamesReverseCalldata 組裝 reverse(address) calldata。
func TACNamesReverseCalldata(addr0x string) []byte {
	out := append([]byte{}, SelectorNSReverse...)
	return append(out, IntToBytes(addrToInt(addr0x), 32)...)
}

// TACNamesNameExistsCalldata 組裝 nameExists(bytes32) calldata。
func TACNamesNameExistsCalldata(labelHash []byte) []byte {
	out := append([]byte{}, SelectorNSNameExists...)
	return append(out, IntToBytes(BytesToInt(labelHash), 32)...)
}

// TACNamesSupportsInterfaceCalldata 組裝 supportsInterface(bytes4) calldata。
func TACNamesSupportsInterfaceCalldata(interfaceID []byte) []byte {
	out := append([]byte{}, SelectorNSSupportsInterface...)
	return append(out, IntToBytes(BytesToInt(interfaceID), 32)...)
}
