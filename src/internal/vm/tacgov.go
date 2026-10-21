package vm

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// TACGovernor：鏈上治理（DAO）合約。
// 精簡 Compound 風格治理：任何帳戶可提案（propose），任何帳戶可一人一票投票
// （vote，support=1 贊成 / 0 反對），多數決（support > oppose）且未執行時可
// 由任何人 execute 觸發對目標地址的 TACm 轉帳執行。
//
// 記憶體/Slot 佈局：
//
//	slot1  = proposalCount（下一個 proposal id，1-based）
//	slot2  = 部署者（管理員，保留）
//	slot3  = mapping(pid → creator)
//	slot4  = mapping(pid → supportCount)
//	slot5  = mapping(pid → opposeCount)
//	slot6  = mapping(pid → executed)
//	slot7  = mapping(pid → target)
//	slot8  = mapping(pid → value)
//	slot9  = nested(pid, voter → voted bool)（防重複投票）
//	slot10 = mapping(pid → startBlock)
//
// Selectors：
//
//	propose(uint256,uint256)  -> 0x5b4c9c59
//	vote(uint256,uint256)     -> 0xb384abef
//	execute(uint256)          -> 0xfe0d94c1
//	proposalInfo(uint256)     -> 0xae092e89
//	supportsInterface(bytes4) -> 0x01ffc9a7

var (
	SelectorGovPropose = []byte{0x5b, 0x4c, 0x9c, 0x59} // propose(uint256,uint256)
	SelectorGovVote    = []byte{0xb3, 0x84, 0xab, 0xef} // vote(uint256,uint256)
	SelectorGovExecute = []byte{0xfe, 0x0d, 0x94, 0xc1} // execute(uint256)
	SelectorGovInfo    = []byte{0xae, 0x09, 0x2e, 0x89} // proposalInfo(uint256)
	SelectorGovSupports = []byte{0x01, 0xff, 0xc9, 0xa7} // supportsInterface(bytes4)
)

// TACGovernorRuntime 回傳治理合約 runtime 位元組碼（固定）。
func TACGovernorRuntime() []byte {
	selOrder := [][]byte{
		SelectorGovPropose, SelectorGovVote, SelectorGovExecute,
		SelectorGovInfo, SelectorGovSupports,
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
		tacGovSegPropose(), tacGovSegVote(), tacGovSegExecute(),
		tacGovSegInfo(), tacGovSegSupportsIf(),
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

// TACGovernorInit 產生部署 init：記錄部署者到 slot2，CODECOPY runtime 並 RETURN。
func TACGovernorInit() []byte {
	runtime := TACGovernorRuntime()
	var b []byte
	b = append(b, codeCaller...) // CALLER
	b = append(b, 0x60, 0x02)    // key 2
	b = append(b, 0x55)          // SSTORE slot2=部署者
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

// govMapSlot：計算 mapping(pid → value) 的 slot（pid 在 stack 頂）。
func govMapSlot(base byte) []byte {
	return mapSlotCode(nil, base)
}

// tacGovSegPropose: propose(target, value)——任何帳戶。
// 回傳新 proposal id。
func tacGovSegPropose() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	// newCount = SLOAD(1)+1；存 memory[0]
	b = append(b, 0x60, 0x01, 0x54) // SLOAD(1)
	b = append(b, 0x60, 0x01, 0x01) // ADD
	b = append(b, 0x60, 0x00, 0x52) // MSTORE(0, new)
	// SSTORE(1, new)：MLOAD(0) 後頂=new=value，PUSH1 1 後頂=1=key → 直接 SSTORE。
	b = append(b, 0x60, 0x00, 0x51) // MLOAD(0) → [new]
	b = append(b, 0x60, 0x01)       // [1, new]（棧頂 key=1、次=new=value）
	b = append(b, 0x55)             // SSTORE(1, new)
	// 存 creator：slot3[new]=CALLER
	b = append(b, 0x60, 0x00, 0x51) // MLOAD(0) → [new]
	b = append(b, govMapSlot(3)...) // → [slot3]
	b = append(b, codeCaller...)    // → [caller, slot3]
	b = append(b, 0x90)             // SWAP1 → [slot3, caller]（棧頂 key）
	b = append(b, 0x55)             // SSTORE(slot3, caller)
	// 存 target：slot7[new]=calldata(4)
	b = append(b, 0x60, 0x00, 0x51) // MLOAD(0)
	b = append(b, govMapSlot(7)...)
	b = append(b, codeCalldata4...) // [target, slot7]
	b = append(b, 0x90)             // SWAP1
	b = append(b, 0x55)             // SSTORE
	// 存 value：slot8[new]=calldata(36)
	b = append(b, 0x60, 0x00, 0x51)
	b = append(b, govMapSlot(8)...)
	b = append(b, codeCalldata24...) // [value, slot8]
	b = append(b, 0x90)              // SWAP1
	b = append(b, 0x55)              // SSTORE
	// 存 startBlock：slot10[new]=NUMBER
	b = append(b, 0x60, 0x00, 0x51)
	b = append(b, govMapSlot(10)...)
	b = append(b, 0x43) // NUMBER → [block, slot10]
	b = append(b, 0x90) // SWAP1
	b = append(b, 0x55) // SSTORE
	// 回傳 new
	b = append(b, 0x60, 0x00, 0x51) // MLOAD(0)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// tacGovSegVote: vote(pid, support)——一人一票，重複投票 revert。
func tacGovSegVote() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B) // JUMPDEST
	// 1. slot9 = nested(pid, CALLER, 9)
	b = append(b, codeCalldata4...)                  // [pid]
	b = append(b, nestedSlotCode(nil, codeCaller, 9)...) // [slot9]
	b = append(b, 0x80)                              // DUP1 → [slot9, slot9]
	b = append(b, 0x54)                              // SLOAD → [voted, slot9]
	// voted != 0 → fail
	failPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI fail
	jumps = append(jumps, segJump{pos: failPos, target: 0})
	// 2. 標記 voted=1（棧頂 slot9 仍在）
	b = append(b, 0x60, 0x01) // [1, slot9]
	b = append(b, 0x90)       // SWAP1 → [slot9, 1]（棧頂 key）
	b = append(b, 0x55)       // SSTORE(slot9, 1)
	// 3. supportCount[pid] += support
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(4)...) // [slot4]
	b = append(b, 0x80)             // DUP1
	b = append(b, 0x54)             // SLOAD → [cur, slot4]
	b = append(b, codeCalldata24...) // [sup, cur, slot4]
	b = append(b, 0x01)             // ADD → [cur+sup, slot4]
	b = append(b, 0x90)             // SWAP1
	b = append(b, 0x55)             // SSTORE
	// 4. opposeCount[pid] += 1-support（EVM SUB=次頂-頂；push 1 先、sup 後 → 頂=sup、次=1 → 1-sup）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(5)...) // [slot5]
	b = append(b, 0x80)             // DUP1
	b = append(b, 0x54)             // SLOAD → [cur, slot5]
	b = append(b, 0x60, 0x01)       // [1, cur, slot5]
	b = append(b, codeCalldata24...) // [sup, 1, cur, slot5]（頂=sup）
	b = append(b, 0x03)             // SUB → 1-sup
	b = append(b, 0x01)             // ADD → [cur+1-sup, slot5]
	b = append(b, 0x90)             // SWAP1
	b = append(b, 0x55)             // SSTORE
	// 5. 成功回傳 1
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	// fail:
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	failIdx := nthJumpDest(b, 1)
	for i := range jumps {
		jumps[i].target = failIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacGovSegExecute: execute(pid)——多數決（support>oppose）且未執行時，
// 對 slot7 目標轉 slot8 金額；成功後標記 executed。
func tacGovSegExecute() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B) // JUMPDEST
	// 1. executed==0 檢查（slot6[pid]）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(6)...) // [slot6]
	b = append(b, 0x54)             // SLOAD → [exe]
	failPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI fail（已執行）
	jumps = append(jumps, segJump{pos: failPos, target: 0})
	// 2. support > oppose（slot4 vs slot5）；sup<=opp → fail
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(4)...)
	b = append(b, 0x54) // [sup]
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(5)...)
	b = append(b, 0x54) // [opp, sup]
	b = append(b, 0x11) // GT → [sup>opp]
	b = append(b, 0x15) // ISZERO → [sup<=opp]
	fail2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI fail（未過半）
	jumps = append(jumps, segJump{pos: fail2Pos, target: 0})
	// 3. CALL(target, value)（value 從 slot8、to 從 slot7 讀取）
	b = append(b, 0x60, 0x00) // outSize=0
	b = append(b, 0x60, 0x00) // outOff=0
	b = append(b, 0x60, 0x00) // inSize=0
	b = append(b, 0x60, 0x00) // inOff=0
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(8)...)
	b = append(b, 0x54) // SLOAD → value
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(7)...)
	b = append(b, 0x54) // SLOAD → to
	b = append(b, 0x61, 0xFF, 0xFF) // PUSH2 gas=0xFFFF
	b = append(b, 0xF1)             // CALL → [success]
	b = append(b, 0x15)             // ISZERO → [!success]
	fail3Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI fail（CALL 失敗）
	jumps = append(jumps, segJump{pos: fail3Pos, target: 0})
	// 4. 標記 executed=1
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(6)...) // [slot6]
	b = append(b, 0x60, 0x01)       // [1, slot6]
	b = append(b, 0x90)             // SWAP1
	b = append(b, 0x55)             // SSTORE(slot6, 1)
	// 5. 回傳 1
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	// fail:
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	failIdx := nthJumpDest(b, 1)
	for i := range jumps {
		jumps[i].target = failIdx
	}
	return builtSeg{code: b, jumps: jumps}
}

// tacGovSegInfo: proposalInfo(pid)——回傳 6 個 32-byte word：
// creator, support, oppose, executed, target, value。
// 注意：mapSlotCode 會污染 mem[0..63]，故各值寫入高位區 mem[256..]，
// 避免後續 slot 計算覆寫先前寫入的 word。
func tacGovSegInfo() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	// mem[256]=creator（offset 0x0100）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(3)...)
	b = append(b, 0x54)
	b = append(b, 0x61, 0x01, 0x00, 0x52) // MSTORE(256, creator)
	// mem[288]=support（offset 0x0120）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(4)...)
	b = append(b, 0x54)
	b = append(b, 0x61, 0x01, 0x20, 0x52)
	// mem[320]=oppose（offset 0x0140）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(5)...)
	b = append(b, 0x54)
	b = append(b, 0x61, 0x01, 0x40, 0x52)
	// mem[352]=executed（offset 0x0160）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(6)...)
	b = append(b, 0x54)
	b = append(b, 0x61, 0x01, 0x60, 0x52)
	// mem[384]=target（offset 0x0180）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(7)...)
	b = append(b, 0x54)
	b = append(b, 0x61, 0x01, 0x80, 0x52)
	// mem[416]=value（offset 0x01A0）
	b = append(b, codeCalldata4...)
	b = append(b, govMapSlot(8)...)
	b = append(b, 0x54)
	b = append(b, 0x61, 0x01, 0xA0, 0x52)
	// RETURN(256,192)：push size=192 先、offset=256 後
	b = append(b, 0x60, 0xC0, 0x61, 0x01, 0x00, 0xF3)
	return builtSeg{code: b}
}

// tacGovSegSupportsIf: supportsInterface(bytes4)——回 0（未宣告 ERC-165）。
func tacGovSegSupportsIf() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, 0x60, 0x00) // PUSH1 0
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// govCalldata 組裝「selector＋固定 32-byte 參數」calldata。
func govCalldata(sel []byte, args ...*big.Int) []byte {
	b := append([]byte{}, sel...)
	for _, a := range args {
		b = append(b, IntToBytes(a, 32)...)
	}
	return b
}

// TACGovProposeCalldata：propose(target, value)。
func TACGovProposeCalldata(target, value *big.Int) []byte {
	return govCalldata(SelectorGovPropose, target, value)
}

// TACGovVoteCalldata：vote(pid, support)。
func TACGovVoteCalldata(pid, support *big.Int) []byte {
	return govCalldata(SelectorGovVote, pid, support)
}

// TACGovExecuteCalldata：execute(pid)。
func TACGovExecuteCalldata(pid *big.Int) []byte {
	return govCalldata(SelectorGovExecute, pid)
}

// TACGovInfoCalldata：proposalInfo(pid)。
func TACGovInfoCalldata(pid *big.Int) []byte {
	return govCalldata(SelectorGovInfo, pid)
}

// TACGovTargetWord 把 0x 20-byte 地址轉為 32-byte 值。
func TACGovTargetWord(to0x string) (*big.Int, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(to0x), "0x"))
	if err != nil || len(b) != 20 {
		return nil, fmt.Errorf("tacgov: target 需為 0x 開頭 20-byte 地址")
	}
	return BytesToInt(b), nil
}
