package vm

// M135-A：ERC-1155 多代幣標準（Multi Token）——balanceOf / balanceOfBatch /
// mint / safeTransferFrom / safeBatchTransferFrom / setApprovalForAll /
// isApprovedForAll / uri / supportsInterface。
// 與 ERC-20/721 相同：runtime 為組裝 bytecode，storage 用嵌套 mapping。
//
// Slot 配置：
//   balance[id][owner] = keccak(owner ‖ keccak(id ‖ 1))（base=1）
//   operatorApproval[owner][operator] = keccak(operator ‖ keccak(owner ‖ 2))（base=2）
//   uri[id] = keccak(id ‖ 3)（base=3）
//   name=slot1、symbol=slot2

import "math/big"
var (
	codeCalldata100 = []byte{0x60, 0x64, 0x35} // CALLDATALOAD(100)
)


// ERC-1155 selector（keccak256 前 4 bytes）。
var (
	Selector1155BalanceOf           = []byte{0x00, 0xfd, 0xd5, 0x8e} // balanceOf(address,uint256)
	Selector1155BalanceOfBatch      = []byte{0x4e, 0x12, 0x73, 0xf4} // balanceOfBatch(address[],uint256[])
	Selector1155SetApprovalForAll   = []byte{0xa2, 0x2c, 0xb4, 0x65} // setApprovalForAll(address,bool)
	Selector1155IsApprovedForAll    = []byte{0xe9, 0x85, 0xe9, 0xc5} // isApprovedForAll(address,address)
	Selector1155SafeTransferFrom    = []byte{0xf2, 0x42, 0x43, 0x2a} // safeTransferFrom(address,address,uint256,uint256,bytes)
	Selector1155SafeBatchTransfer   = []byte{0x2e, 0xb2, 0xc2, 0xf6} // safeBatchTransferFrom(address,address,uint256[],uint256[],bytes)
	Selector1155URI                 = []byte{0x0e, 0x89, 0x34, 0x1c} // uri(uint256)
	Selector1155SupportsInterface   = []byte{0x01, 0xff, 0xc9, 0xa7} // supportsInterface(bytes4)
	Selector1155Mint                = []byte{0x15, 0x6e, 0x29, 0xf6} // mint(address,uint256,uint256)（自訂）
	Topic1155TransferSingle         = []byte{0xc3, 0xd5, 0x81, 0x68, 0xc5, 0xae, 0x73, 0x97, 0x73, 0x1d, 0x06, 0x3d, 0x5b, 0xbf, 0x3d, 0x65, 0x78, 0x54, 0x42, 0x73, 0x43, 0xf4, 0xc0, 0x83, 0x24, 0x0f, 0x7a, 0xac, 0xaa, 0x2d, 0x0f, 0x62}
	Topic1155TransferBatch          = []byte{0x4a, 0x39, 0xdc, 0x06, 0xd4, 0xc0, 0xdb, 0xc6, 0x4b, 0x70, 0xaf, 0x90, 0xfd, 0x69, 0x8a, 0x23, 0x3a, 0x51, 0x8a, 0xa5, 0xd0, 0x7e, 0x59, 0x5d, 0x98, 0x3b, 0x8c, 0x05, 0x26, 0xc8, 0xf7, 0xfb}
	Topic1155ApprovalForAll         = []byte{0x17, 0x30, 0x7e, 0xab, 0x39, 0xab, 0x61, 0x07, 0xe8, 0x89, 0x98, 0x45, 0xad, 0x3d, 0x59, 0xbd, 0x96, 0x53, 0xf2, 0x00, 0xf2, 0x20, 0x92, 0x04, 0x89, 0xca, 0x2b, 0x59, 0x37, 0x69, 0x6c, 0x31}
	interface1155ID                 = []byte{0xd9, 0xb6, 0x7a, 0x26}
	interface165ID                  = []byte{0x01, 0xff, 0xc9, 0xa7}
)

// Erc1155Runtime 回傳標準 ERC-1155 runtime（selector 分派）。
func Erc1155Runtime() []byte {
	selOrder := [][]byte{
		Selector1155BalanceOf, Selector1155BalanceOfBatch,
		Selector1155SetApprovalForAll, Selector1155IsApprovedForAll,
		Selector1155SafeTransferFrom, Selector1155SafeBatchTransfer,
		Selector1155URI, Selector1155SupportsInterface, Selector1155Mint,
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
		erc1155SegBalanceOf(), erc1155SegBalanceOfBatch(),
		erc1155SegSetApprovalForAll(), erc1155SegIsApprovedForAll(),
		erc1155SegSafeTransferFrom(), erc1155SegSafeBatchTransfer(),
		erc1155SegURI(), erc1155SegSupportsInterface(), erc1155SegMint(),
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

// erc1155SegBalanceOf: balanceOf(owner, id) → nested(owner,id,1) → SLOAD → RETURN(0,32)
func erc1155SegBalanceOf() builtSeg {
	b := []byte{0x5B}
	b = append(b, nestedSlotCode(codeCalldata24 /*id*/, codeCalldata4 /*owner*/, 1)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

// erc1155SegBalanceOfBatch: balanceOfBatch(owners[], ids[]) → 逐筆讀取回傳陣列。
// calldata：ownersOffset@4、idsOffset@36、len@68。
// 回傳 mem[0..outLen]（32 + 32*len，每項 32-byte balance）。
func erc1155SegBalanceOfBatch() builtSeg {
	var b []byte
	var jumps []segJump
	// 進場：mem[288]=len、mem[320]=i、mem[128]=owner、mem[160]=id。
	b = append(b, 0x5B)                     // JUMPDEST top
	b = append(b, 0x60, 0x44, 0x35)         // CALLDATALOAD(68) → len（tail 起點）
	b = append(b, 0x61, 0x01, 0x20, 0x52)   // MSTORE(288, len)
	b = append(b, 0x60, 0x00, 0x61, 0x01, 0x40, 0x52) // MSTORE(320, 0)（i=0）
	loopTop := len(b)
	b = append(b, 0x5B)                     // JUMPDEST loopTop
	// 條件：i < len（LT：棧頂<次頂 → MLOAD(320) MLOAD(288) → i<len）
	b = append(b, 0x61, 0x01, 0x40, 0x51, 0x61, 0x01, 0x20, 0x51, 0x10, 0x15)
	afterPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)   // JUMPI after（i>=len 才跳）
	afterJump := segJump{pos: afterPos, target: 0}
	// owner = calldataload(calldataload(4) + 32 + 32*i)
	b = append(b, 0x61, 0x01, 0x40, 0x51, 0x60, 0x20, 0x02, 0x60, 0x20, 0x01, 0x60, 0x04, 0x35, 0x01, 0x35)
	b = append(b, 0x60, 0x80, 0x52)         // MSTORE(128, owner)
	// id = calldataload(calldataload(24) + 32 + 32*i)
	b = append(b, 0x61, 0x01, 0x40, 0x51, 0x60, 0x20, 0x02, 0x60, 0x20, 0x01, 0x60, 0x24, 0x35, 0x01, 0x35)
	b = append(b, 0x60, 0xA0, 0x52)         // MSTORE(160, id)
	// slot = keccak(owner ‖ keccak(id ‖ 1))
	b = append(b, 0x60, 0xA0, 0x51, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x01, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)
	b = append(b, 0x60, 0x80, 0x51, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)
	b = append(b, 0x54)                     // SLOAD → val
	// mem[64 + 32*i] = val（items 從 64 開始，留 offset/length）
	b = append(b, 0x61, 0x01, 0x40, 0x51, 0x60, 0x20, 0x02, 0x60, 0x40, 0x01, 0x52)
	// i++
	b = append(b, 0x61, 0x01, 0x40, 0x51, 0x60, 0x01, 0x01, 0x61, 0x01, 0x40, 0x52)
	// jump loopTop（組裝時 patch 為絕對位址）
	b = append(b, 0x61, 0x00, 0x00, 0x56)
	jumpLoopPos := len(b) - 3
	jumps = append(jumps, segJump{pos: jumpLoopPos, target: loopTop})
	// after：ABI uint256[] = [offset(32), length(32), items...] → RETURN(0, 64+32*len)
	after := len(b)
	afterJump.target = after
	jumps = append(jumps, afterJump)
	b = append(b, 0x5B)
	b = append(b, 0x60, 0x20, 0x60, 0x00, 0x52)  // mem[0]=32（data 起點）
	b = append(b, 0x61, 0x01, 0x20, 0x51, 0x60, 0x20, 0x52) // mem[32]=len
	b = append(b, 0x61, 0x01, 0x20, 0x51, 0x60, 0x20, 0x02, 0x60, 0x40, 0x01) // 64+32*len
	b = append(b, 0x60, 0x00, 0xF3)
	return builtSeg{code: b, jumps: jumps}
}

// erc1155SegMint: mint(to, id, amount)——balance[to][id] += amount、
// LOG4(TransferSingle, caller, 0, to, id‖amount)、回傳 1。
func erc1155SegMint() builtSeg {
	var b []byte
	b = append(b, 0x5B) // JUMPDEST
	// slot = nested(id@24, to@4, 1)
	b = append(b, nestedSlotCode(codeCalldata24, codeCalldata4, 1)...)
	b = append(b, 0x80)                       // DUP1
	b = append(b, 0x54)                       // bal
	b = append(b, codeCalldata68...)          // amount@68
	b = append(b, 0x01)                       // bal+amount
	b = append(b, 0x90)                       // SWAP1 → [bal+amount, slot]（key 回頂）
	b = append(b, 0x55)                       // SSTORE
	// LOG4(TransferSingle, caller, 0, to, data=id‖amount)
	b = append(b, codeCalldata68...)
	b = append(b, 0x60, 0x20, 0x52) // mem[32]=amount
	b = append(b, codeCalldata24...)
	b = append(b, 0x60, 0x00, 0x52) // mem[0]=id
	b = append(b, push32(selNum(Topic1155TransferSingle))...) // t1
	b = append(b, codeCaller...)                // t2=operator
	b = append(b, 0x60, 0x00)                   // t3=from=0
	b = append(b, codeCalldata4...)             // t4=to
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0xA4) // LOG4(size=64, offset=0)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// erc1155SegSafeTransferFrom: safeTransferFrom(from, to, id, amount, data)
// ——require (from==caller 或 approvalForAll[from][caller]) 且 balance[from][id]>=amount；
// 轉移、LOG4(TransferSingle, caller, from, to, id‖amount)、回傳 1。
func erc1155SegSafeTransferFrom() builtSeg {
	var b []byte
	var jumps []segJump
	// 1) 授權：from==caller 或 approvalForAll[from][caller]!=0
	b = append(b, 0x5B)
	b = append(b, codeCalldata4...)
	b = append(b, codeCaller...)
	b = append(b, 0x14) // from==caller
	aPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)
	jumps = append(jumps, segJump{pos: aPos, target: 0})
	// 分支 B：approvalForAll[from][caller]（nested：owner=from@4、operator=caller、base=2）
	b = append(b, nestedSlotCode(codeCalldata4, codeCaller, 2)...)
	b = append(b, 0x54) // approved
	bPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI ok（approved!=0）
	jumps = append(jumps, segJump{pos: bPos, target: 0})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	// ok（auth）
	ok := len(b)
	for i := range jumps {
		if jumps[i].pos == aPos || jumps[i].pos == bPos {
			jumps[i].target = ok
		}
	}
	b = append(b, 0x5B)
	// 2) 餘額檢查 + 扣款：balance[from][id] -= amount
	b = append(b, nestedSlotCode(codeCalldata68, codeCalldata4, 1)...) // fromSlot（id@68）
	b = append(b, 0x80, 0x54)                                          // [fromSlot, balFrom]
	b = append(b, codeCalldata100...)                                  // amount@100
	b = append(b, 0x11)                                                // GT → balFrom>amount（有餘額）
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI ok2
	jumps = append(jumps, segJump{pos: okPos, target: 0})
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	ok2 := len(b)
	for i := range jumps {
		if jumps[i].pos == okPos {
			jumps[i].target = ok2
		}
	}
	b = append(b, 0x5B)                         // ok2: [fromSlot]
	// 重算 balFrom 並扣款：balFrom - amount
	b = append(b, 0x80, 0x54)                   // [fromSlot, balFrom]
	b = append(b, codeCalldata100...)           // [fromSlot, balFrom, amount]
	b = append(b, 0x03)                         // [fromSlot, balFrom-amount]（SUB）
	b = append(b, 0x90, 0x55)                   // SWAP1 → [newBal, fromSlot] → SSTORE
	// 3) to 加款：balance[to][id] += amount
	b = append(b, nestedSlotCode(codeCalldata68, codeCalldata24, 1)...) // toSlot（id@68）
	b = append(b, 0x80, 0x54)                                          // [toSlot, balTo]
	b = append(b, codeCalldata100...)                                  // amount
	b = append(b, 0x01)                                                // balTo+amount
	b = append(b, 0x90, 0x55)                                          // SSTORE
	// 4) LOG4(TransferSingle, caller, from, to, id‖amount)
	b = append(b, codeCalldata100...)
	b = append(b, 0x60, 0x20, 0x52) // mem[32]=amount
	b = append(b, codeCalldata68...)
	b = append(b, 0x60, 0x00, 0x52) // mem[0]=id
	b = append(b, push32(selNum(Topic1155TransferSingle))...)
	b = append(b, codeCaller...)   // t2=caller
	b = append(b, codeCalldata4...) // t3=from
	b = append(b, codeCalldata24...) // t4=to
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0xA4)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b, jumps: jumps}
}

// erc1155SegSetApprovalForAll: setApprovalForAll(operator, approved)
// ——approvalForAll[caller][operator]=approved、LOG2(ApprovalForAll, caller, operator, approved)、回 1。
func erc1155SegSetApprovalForAll() builtSeg {
	var b []byte
	b = append(b, 0x5B)
	b = append(b, nestedSlotCode(codeCaller, codeCalldata4, 2)...) // slot
	b = append(b, codeCalldata24...)                               // approved（value）
	b = append(b, 0x90, 0x55)                                      // SWAP1 SSTORE
	// LOG3(ApprovalForAll, caller, operator, data=approved)
	b = append(b, codeCalldata24...)                            // approved（data）
	b = append(b, 0x60, 0x00, 0x52)                             // mem[0]=approved
	b = append(b, push32(selNum(Topic1155ApprovalForAll))...)    // t1
	b = append(b, codeCaller...)                                 // t2=owner
	b = append(b, codeCalldata4...)                              // t3=operator
	b = append(b, 0x60, 0x20, 0x60, 0x00, 0xA3)                  // LOG3(size=32, offset=0)
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// erc1155SegIsApprovedForAll: isApprovedForAll(owner, operator) → RETURN(0,32)
func erc1155SegIsApprovedForAll() builtSeg {
	b := []byte{0x5B}
	b = append(b, nestedSlotCode(codeCalldata4, codeCalldata24, 2)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

// erc1155SegURI: uri(id) → mapping(id,3) → SLOAD → RETURN(0,32)
func erc1155SegURI() builtSeg {
	b := []byte{0x5B}
	b = append(b, mapSlotCode(codeCalldata4, 3)...)
	b = append(b, 0x54)
	return builtSeg{code: append(b, RET32...)}
}

// erc1155SegSupportsInterface: supportsInterface(iface) → iface∈{0xd9b67a26,0x01ffc9a7} ? 1 : 0
func erc1155SegSupportsInterface() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)
	// iface = calldataload(4)
	b = append(b, codeCalldata4...)
	// iface == interface1155ID
	b = append(b, 0x80)
	b = append(b, push32(selNum(interface1155ID))...)
	b = append(b, 0x14) // DUP1 push EQ
	okPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57) // JUMPI ok
	jumps = append(jumps, segJump{pos: okPos, target: 0})
	// iface == interface165ID（棧仍 [iface]）
	b = append(b, push32(selNum(interface165ID))...)
	b = append(b, 0x14) // EQ
	ok2Pos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)
	jumps = append(jumps, segJump{pos: ok2Pos, target: 0})
	// 0（false 分支）
	b = append(b, 0x60, 0x00)
	endJumpPos := len(b)
	b = append(b, 0x61, 0x00, 0x00, 0x56) // PUSH2 end JUMP
	// ok：1
	ok := len(b)
	for i := range jumps {
		if i < 2 {
			jumps[i].target = ok
		}
	}
	b = append(b, 0x5B, 0x60, 0x01) // JUMPDEST ok; PUSH1 1
	end := len(b)
	b = append(b, 0x5B) // JUMPDEST end
	b = append(b, RET32...)
	// end 跳轉：inline（end 為段內偏移需轉絕對——交由收集器統一 patch，故用 segJump）
	// 由於收集器只處理 seg.jumps，end 跳轉也加入清單（pos=endJumpPos, target=end）
	jumps = append(jumps, segJump{pos: endJumpPos, target: end})
	return builtSeg{code: b, jumps: jumps}
}

// erc1155SegSafeBatchTransfer: safeBatchTransferFrom(from, to, ids[], amounts[], data)
// ——逐 id 轉移、LOG4(TransferBatch, caller, from, to)、回 1。
// calldata：from@4、to@24、idsOffset@44、amountsOffset@64、len@84。
func erc1155SegSafeBatchTransfer() builtSeg {
	var b []byte
	var jumps []segJump
	b = append(b, 0x5B)
	// mem[288]=idsOff（CALLDATALOAD(68)）；mem[320]=amountsOff（CALLDATALOAD(100)）
	// 槽位刻意遠離 from(128)/to(160)/id(192)/amount(224) 避免記憶體重疊
	b = append(b, 0x60, 0x44, 0x35, 0x61, 0x01, 0x20, 0x52)
	b = append(b, 0x60, 0x64, 0x35, 0x61, 0x01, 0x40, 0x52)
	// mem[64]=len = CALLDATALOAD(idsOff)
	b = append(b, 0x61, 0x01, 0x20, 0x51, 0x35, 0x60, 0x40, 0x52)
	b = append(b, 0x60, 0x00, 0x60, 0x60, 0x52)        // mem[96]=0（i）
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x80, 0x52)
	b = append(b, codeCalldata24...)
	b = append(b, 0x60, 0xA0, 0x52)
	loopTop := len(b)
	b = append(b, 0x5B) // JUMPDEST loopTop
	b = append(b, 0x60, 0x60, 0x51, 0x60, 0x40, 0x51, 0x10, 0x15) // i >= len
	afterPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)
	afterJump := segJump{pos: afterPos, target: 0}
	// id = calldataload(MLOAD(288) + 32 + 32*i)
	b = append(b, 0x60, 0x60, 0x51, 0x60, 0x20, 0x02, 0x60, 0x20, 0x01, 0x61, 0x01, 0x20, 0x51, 0x01, 0x35)
	b = append(b, 0x60, 0xC0, 0x52) // mem[192]=id
	// amount = calldataload(MLOAD(320) + 32 + 32*i)
	b = append(b, 0x60, 0x60, 0x51, 0x60, 0x20, 0x02, 0x60, 0x20, 0x01, 0x61, 0x01, 0x40, 0x51, 0x01, 0x35)
	b = append(b, 0x60, 0xE0, 0x52) // mem[224]=amount
	// 餘額檢查：balance[from][id] >= amount
	b = append(b, 0x60, 0xC0, 0x51, 0x60, 0x00, 0x52) // mem[0]=id
	b = append(b, 0x60, 0x01, 0x60, 0x20, 0x52)       // mem[32]=1
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)       // inner = keccak(id‖1)
	b = append(b, 0x60, 0x80, 0x51, 0x60, 0x00, 0x52) // mem[0]=from
	b = append(b, 0x60, 0x20, 0x52)                   // mem[32]=inner
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)       // slot = keccak(from‖inner)
	b = append(b, 0x54)             // balFrom
	b = append(b, 0x60, 0xE0, 0x51) // amount
	b = append(b, 0x10, 0x15)       // LT ISZERO → balFrom >= amount
	balPos := len(b) + 1
	b = append(b, 0x61, 0x00, 0x00, 0x57)
	balJump := segJump{pos: balPos, target: 0}
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT
	// okBal：from 扣款（重算 slot）
	okBal := len(b)
	balJump.target = okBal
	jumps = append(jumps, balJump)
	b = append(b, 0x5B)
	b = append(b, 0x60, 0xC0, 0x51, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x01, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)
	b = append(b, 0x60, 0x80, 0x51, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)
	b = append(b, 0x80, 0x54)       // [slot, bal]
	b = append(b, 0x60, 0xE0, 0x51) // [slot, bal, amount]
	b = append(b, 0x03)             // SUB
	b = append(b, 0x90, 0x55)       // SSTORE
	// to 加款（重算 slot：keccak(to‖inner)）
	b = append(b, 0x60, 0xC0, 0x51, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x01, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)
	b = append(b, 0x60, 0xA0, 0x51, 0x60, 0x00, 0x52)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, 0x60, 0x40, 0x60, 0x00, 0x20)
	b = append(b, 0x80, 0x54)       // [slot, bal]
	b = append(b, 0x60, 0xE0, 0x51) // amount
	b = append(b, 0x01)             // ADD
	b = append(b, 0x90, 0x55)       // SSTORE
	// i++
	b = append(b, 0x60, 0x60, 0x51, 0x60, 0x01, 0x01, 0x60, 0x60, 0x52)
	// jump loopTop（組裝時 patch 為絕對位址）
	b = append(b, 0x61, 0x00, 0x00, 0x56)
	jumpLoopPos := len(b) - 3
	jumps = append(jumps, segJump{pos: jumpLoopPos, target: loopTop})
	// after：LOG4(TransferBatch, caller, from, to, data=ids‖amounts)
	after := len(b)
	afterJump.target = after
	jumps = append(jumps, afterJump)
	b = append(b, 0x5B)
	// 複製 calldata tail（idsOff 起，64+64n bytes）到 memory[0..]
	b = append(b, 0x60, 0x40, 0x51, 0x61, 0x00, 0x40, 0x02, 0x60, 0x40, 0x01) // size = 64+64*n
	b = append(b, 0x60, 0x70, 0x51)   // dataOff = idsOff
	b = append(b, 0x60, 0x00)         // memOff = 0
	b = append(b, 0x37)               // CALLDATACOPY
	b = append(b, push32(selNum(Topic1155TransferBatch))...)
	b = append(b, codeCaller...)
	b = append(b, 0x60, 0x80, 0x51) // from
	b = append(b, 0x60, 0xA0, 0x51) // to
	b = append(b, 0x60, 0x40, 0x51, 0x61, 0x00, 0x40, 0x02, 0x60, 0x40, 0x01) // size
	b = append(b, 0x60, 0x00, 0xA4) // LOG4(size, 0)
	b = append(b, codeReturnTrue...)
	return builtSeg{code: b, jumps: jumps}
}

// Erc1155InitWithMeta 回傳帶元資料（name/symbol）的部署位元組碼：
// name→slot1、symbol→slot2（右對齊 32-byte）、回傳 runtime。
func Erc1155InitWithMeta(name, symbol []byte) []byte {
	runtime := Erc1155Runtime()
	var b []byte
	b = append(b, push32(new(big.Int).SetBytes(name))...)
	b = append(b, 0x60, 0x01) // key 1
	b = append(b, 0x55)       // SSTORE
	b = append(b, push32(new(big.Int).SetBytes(symbol))...)
	b = append(b, 0x60, 0x02) // key 2
	b = append(b, 0x55)       // SSTORE
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

// Erc1155BalanceOfCalldata 組裝 balanceOf(owner, id) calldata。
func Erc1155BalanceOfCalldata(owner0x string, id *big.Int) []byte {
	out := append([]byte{}, Selector1155BalanceOf...)
	out = append(out, IntToBytes(addrToInt(owner0x), 32)...)
	return append(out, IntToBytes(id, 32)...)
}

// Erc1155MintCalldata 組裝 mint(to, id, amount) calldata。
func Erc1155MintCalldata(to0x string, id, amount *big.Int) []byte {
	out := append([]byte{}, Selector1155Mint...)
	out = append(out, IntToBytes(addrToInt(to0x), 32)...)
	out = append(out, IntToBytes(id, 32)...)
	return append(out, IntToBytes(amount, 32)...)
}

// Erc1155SafeTransferFromCalldata 組裝 safeTransferFrom(from, to, id, amount, data) calldata。
func Erc1155SafeTransferFromCalldata(from0x, to0x string, id, amount *big.Int) []byte {
	out := append([]byte{}, Selector1155SafeTransferFrom...)
	out = append(out, IntToBytes(addrToInt(from0x), 32)...)
	out = append(out, IntToBytes(addrToInt(to0x), 32)...)
	out = append(out, IntToBytes(id, 32)...)
	out = append(out, IntToBytes(amount, 32)...)
	out = append(out, IntToBytes(big.NewInt(0x80), 32)...) // data offset=128（tail）
	out = append(out, IntToBytes(big.NewInt(0), 32)...)    // data length=0
	return out
}

// Erc1155SetApprovalForAllCalldata 組裝 setApprovalForAll(operator, approved) calldata。
func Erc1155SetApprovalForAllCalldata(operator0x string, approved bool) []byte {
	out := append([]byte{}, Selector1155SetApprovalForAll...)
	out = append(out, IntToBytes(addrToInt(operator0x), 32)...)
	v := big.NewInt(0)
	if approved {
		v = big.NewInt(1)
	}
	return append(out, IntToBytes(v, 32)...)
}

// Erc1155IsApprovedForAllCalldata 組裝 isApprovedForAll(owner, operator) calldata。
func Erc1155IsApprovedForAllCalldata(owner0x, operator0x string) []byte {
	out := append([]byte{}, Selector1155IsApprovedForAll...)
	out = append(out, IntToBytes(addrToInt(owner0x), 32)...)
	return append(out, IntToBytes(addrToInt(operator0x), 32)...)
}

// Erc1155URICalldata 組裝 uri(id) calldata。
func Erc1155URICalldata(id *big.Int) []byte {
	out := append([]byte{}, Selector1155URI...)
	return append(out, IntToBytes(id, 32)...)
}

// Erc1155SupportsInterfaceCalldata 組裝 supportsInterface(iface) calldata。
func Erc1155SupportsInterfaceCalldata(iface []byte) []byte {
	out := append([]byte{}, Selector1155SupportsInterface...)
	return append(out, IntToBytes(selNum(iface), 32)...)
}

// Erc1155BalanceOfBatchCalldata 組裝 balanceOfBatch(owners[], ids[]) calldata（ABI arrays）。
func Erc1155BalanceOfBatchCalldata(owners0x []string, ids []*big.Int) []byte {
	n := len(owners0x)
	out := append([]byte{}, Selector1155BalanceOfBatch...)
	out = append(out, IntToBytes(big.NewInt(68), 32)...)       // owners offset=68（tail 起點）
	out = append(out, IntToBytes(big.NewInt(int64(100+32*n)), 32)...) // ids offset
	out = append(out, IntToBytes(big.NewInt(int64(n)), 32)...) // owners length
	for i := 0; i < n; i++ {
		out = append(out, IntToBytes(addrToInt(owners0x[i]), 32)...)
	}
	out = append(out, IntToBytes(big.NewInt(int64(n)), 32)...) // ids length
	for i := 0; i < n; i++ {
		out = append(out, IntToBytes(ids[i], 32)...)
	}
	return out
}

// Erc1155SafeBatchTransferCalldata 組裝 safeBatchTransferFrom(from, to, ids[], amounts[]) calldata。
func Erc1155SafeBatchTransferCalldata(from0x, to0x string, ids, amounts []*big.Int) []byte {
	n := len(ids)
	out := append([]byte{}, Selector1155SafeBatchTransfer...)
	out = append(out, IntToBytes(addrToInt(from0x), 32)...)
	out = append(out, IntToBytes(addrToInt(to0x), 32)...)
	// 標準 ABI：5 個 head 槽（from/to/ids/amounts/data）＝ 4 + 32*5 = 164
	idsOffset := int64(164)
	amountsOffset := idsOffset + 32 + int64(32*n)
	dataOffset := amountsOffset + 32 + int64(32*n)
	out = append(out, IntToBytes(big.NewInt(idsOffset), 32)...)
	out = append(out, IntToBytes(big.NewInt(amountsOffset), 32)...)
	out = append(out, IntToBytes(big.NewInt(dataOffset), 32)...)
	out = append(out, IntToBytes(big.NewInt(int64(n)), 32)...) // ids length
	for i := 0; i < n; i++ {
		out = append(out, IntToBytes(ids[i], 32)...)
	}
	out = append(out, IntToBytes(big.NewInt(int64(n)), 32)...) // amounts length
	for i := 0; i < n; i++ {
		out = append(out, IntToBytes(amounts[i], 32)...)
	}
	out = append(out, IntToBytes(big.NewInt(0), 32)...) // data length=0
	return out
}
