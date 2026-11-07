package vm

// v133：EIP-1967 可升級代理（OpenZeppelin Proxy 同級語義）。
//
// 主流鏈（ETH/BSC）DeFi 項目普遍採用可升級代理：合約邏輯與狀態分離，
// 升級時僅替換 implementation 槽，狀態（storage）原樣保留。
// 本實作支援：
//   - implementation() → 查詢當前邏輯合約
//   - admin() → 查詢管理員
//   - upgradeTo(addr) → 僅管理員可呼叫，更新 implementation 並 emit Upgraded
//   - changeAdmin(addr) → 僅管理員可呼叫，更新 admin 並 emit AdminChanged
//   - fallback：任何其他 calldata 均 delegatecall 至 implementation（狀態共享）
//
// 槽位與事件 topic 完全遵循 EIP-1967 標準，與主流鏈工具（OpenZeppelin SDK/
// hardhat upgrade）相容。
import (
	"encoding/hex"
	"math/big"
	"strings"
)

// EIP-1967 標準槽位（bytes32(uint256(keccak256('...')) - 1)）。
var (
	proxyImplSlot  = big.NewInt(0) // 於 init 設定為 0x360894...bbc
	proxyAdminSlot = big.NewInt(0) // 0xb531276...6103
	proxyMask20    = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 160), big.NewInt(1))
)

func init() {
	implBytes := proxyMustHex("360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc")
	proxyImplSlot.SetBytes(implBytes)
	adminBytes := proxyMustHex("b53127684a568b3173ae13b9f8a6016e243e63b6e8ee1178d6a717850b5d6103")
	proxyAdminSlot.SetBytes(adminBytes)
}

// Selectors（EIP-1967 Proxy 標準函式）。
var (
	SelectorProxyImpl       = []byte{0x5c, 0x60, 0xda, 0x1b} // implementation()
	SelectorProxyAdmin      = []byte{0xf8, 0x51, 0xa4, 0x40} // admin()
	SelectorProxyUpgradeTo  = []byte{0x36, 0x59, 0xcf, 0xe6} // upgradeTo(address)
	SelectorProxyChangeAdmin = []byte{0x8f, 0x28, 0x39, 0x70} // changeAdmin(address)
)

// Topics（EIP-1967 事件）。
var (
	TopicProxyUpgraded      = proxyMustTopic("bc7cd75a20ee27fd9adebab32041f755214dbc6bffa90cc0225b39da2e5c2d3b")
	TopicProxyAdminChanged  = proxyMustTopic("7e644d79422f17c01e4894b5f4f588d331ebfa28653d42ae832dc59e38c9792f")
)

// ProxyRuntime 回傳 EIP-1967 代理 runtime 位元組碼（selector 分派＋fallback delegatecall）。
func ProxyRuntime() []byte {
	header := []byte{0x60, 0x00, 0x35} // CALLDATALOAD(0)
	header = append(header, push32(new(big.Int).Lsh(big.NewInt(1), 224))...)
	header = append(header, 0x04) // DIV → sel4
	type jumpPatch struct{ pos, label int }
	var patches []jumpPatch
	selOrder := [][]byte{
		SelectorProxyImpl, SelectorProxyAdmin,
		SelectorProxyUpgradeTo, SelectorProxyChangeAdmin,
	}
	d := append([]byte{}, header...)
	for i, sel := range selOrder {
		d = append(d, 0x80) // DUP1
		d = append(d, push32(selNum(sel))...)
		d = append(d, 0x14, 0x61, 0x00, 0x00, 0x57) // EQ / PUSH2 dst / JUMPI
		patches = append(patches, jumpPatch{pos: len(d) - 3, label: i})
	}
	// 未命中 selector → 跳 fallback（最後一段）。
	d = append(d, 0x61, 0x00, 0x00, 0x56) // PUSH2 dst; JUMP
	patches = append(patches, jumpPatch{pos: len(d) - 3, label: 4})

	segs := []builtSeg{
		proxySegImpl(), proxySegAdmin(),
		proxySegUpgradeTo(), proxySegChangeAdmin(), proxySegFallback(),
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

// ProxyInit 回傳代理部署 init 碼：admin=部署者（CALLER）、implementation=arg0，
// 尾部附帶 runtime，init 以 CODECOPY 把 runtime 拷入記憶體並 RETURN（比照 ERC-721 init）。
func ProxyInit(implAddr string) []byte {
	var b []byte
	b = append(b, 0x33) // CALLER
	b = append(b, push32(proxyAdminSlot)...)
	b = append(b, 0x55) // SSTORE adminSlot=caller
	addr := proxyMustHex(implAddr)
	b = append(b, push32(new(big.Int).SetBytes(addr))...)
	b = append(b, push32(proxyImplSlot)...)
	b = append(b, 0x55) // SSTORE implSlot=implAddr
	rt := ProxyRuntime()
	rl := len(rt)
	src := len(b) + 15
	b = append(b, 0x61, byte(rl>>8), byte(rl&0xFF))   // PUSH2 size
	b = append(b, 0x61, byte(src>>8), byte(src&0xFF)) // PUSH2 src
	b = append(b, 0x60, 0x00)                         // PUSH2 dest
	b = append(b, 0x39)                               // CODECOPY（dest 棧頂）
	b = append(b, 0x61, byte(rl>>8), byte(rl&0xFF), 0x60, 0x00, 0xF3) // RETURN(0, rl)
	return append(b, rt...)
}

// proxySegImpl: implementation() → 回 storage[implSlot] 低 160 位址。
func proxySegImpl() builtSeg {
	b := []byte{0x5B} // JUMPDEST
	b = append(b, push32(proxyImplSlot)...)
	b = append(b, 0x54) // SLOAD
	b = append(b, push32(proxyMask20)...)
	b = append(b, 0x16) // AND
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// proxySegAdmin: admin() → 回 storage[adminSlot] 低 160 位址。
func proxySegAdmin() builtSeg {
	b := []byte{0x5B} // JUMPDEST
	b = append(b, push32(proxyAdminSlot)...)
	b = append(b, 0x54)
	b = append(b, push32(proxyMask20)...)
	b = append(b, 0x16)
	b = append(b, RET32...)
	return builtSeg{code: b}
}

// onlyAdmin 檢查（CALLER == storage[adminSlot]），不等則 REVERT(0,0)。
func proxyOnlyAdmin() []byte {
	b := []byte{0x33} // CALLER
	b = append(b, push32(proxyAdminSlot)...)
	b = append(b, 0x54)
	b = append(b, push32(proxyMask20)...)
	b = append(b, 0x16)
	b = append(b, 0x14)             // EQ
	b = append(b, 0x15)             // ISZERO（非 admin=1 → 跳 fail）
	b = append(b, 0x61, 0, 0, 0x57) // PUSH2 fail; JUMPI
	return b
}

// proxySegUpgradeTo: upgradeTo(addr) → onlyAdmin；implSlot=addr；LOG1(Upgraded)；true。
func proxySegUpgradeTo() builtSeg {
	var jumps []segJump
	b := []byte{0x5B} // JUMPDEST
	b = append(b, proxyOnlyAdmin()...)
	jumps = append(jumps, segJump{pos: len(b) - 3, target: 0}) // fail 佔位，稍後補
	// ok：implSlot = arg0（SSTORE：棧頂 key、次頂 value）
	b = append(b, 0x60, 0x04, 0x35) // CALLDATALOAD(4)=newImpl（value）
	b = append(b, push32(proxyImplSlot)...) // key 置頂
	b = append(b, 0x55) // SSTORE implSlot=newImpl
	// emit Upgraded：mem[0]=newImpl
	b = append(b, 0x60, 0x04, 0x35)
	b = append(b, 0x60, 0x00, 0x52) // MSTORE@0
	b = append(b, push32(selNum(TopicProxyUpgraded))...)
	b = append(b, 0x60, 0x20, 0x60, 0x00, 0xA1) // LOG1 size=32 off=0
	// return true
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	// fail：REVERT(0,0)
	failPos := len(b)
	b = append(b, 0x5B) // JUMPDEST（JUMP 目標必須）
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)
	return builtSeg{code: b, jumps: []segJump{{pos: jumps[0].pos, target: failPos}}}
}

// proxySegChangeAdmin: changeAdmin(addr) → onlyAdmin；adminSlot=addr；LOG3(AdminChanged)；true。
func proxySegChangeAdmin() builtSeg {
	var jumps []segJump
	b := []byte{0x5B} // JUMPDEST
	b = append(b, proxyOnlyAdmin()...)
	jumps = append(jumps, segJump{pos: len(b) - 3, target: 0})
	// oldAdmin = storage[adminSlot]
	b = append(b, push32(proxyAdminSlot)...)
	b = append(b, 0x54)
	b = append(b, push32(proxyMask20)...)
	b = append(b, 0x16) // [oldAdmin]
	// newAdmin = arg0；adminSlot=newAdmin（SSTORE：key 頂、value 次）
	b = append(b, 0x60, 0x04, 0x35) // [oldAdmin, newAdmin]
	b = append(b, push32(proxyAdminSlot)...) // key 置頂
	b = append(b, 0x55) // SSTORE adminSlot=newAdmin → 剩 [oldAdmin]
	// emit AdminChanged：mem[0]=newAdmin
	b = append(b, 0x60, 0x04, 0x35)
	b = append(b, 0x60, 0x00, 0x52)
	// LOG3(topic1, oldAdmin, newAdmin)：push 順序 topic1 最早。
	b = append(b, push32(selNum(TopicProxyAdminChanged))...)
	b = append(b, 0x90) // SWAP1 → [topic1, oldAdmin]
	b = append(b, 0x60, 0x04, 0x35) // [topic1, oldAdmin, newAdmin]
	b = append(b, 0x60, 0x20, 0x60, 0x00, 0xA3)
	// return true
	b = append(b, codeReturnTrue...)
	b = append(b, RET32...)
	failPos := len(b)
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD)
	return builtSeg{code: b, jumps: []segJump{{pos: jumps[0].pos, target: failPos}}}
}

// proxySegFallback: 未命中 selector → delegatecall 至 implementation，原樣轉發 calldata/returndata。
func proxySegFallback() builtSeg {
	var jumps []segJump
	b := []byte{0x5B} // JUMPDEST
	// 先把 calldata 拷到 memory[0:cdsize]（CALL 的 input 讀自 memory）。
	b = append(b, 0x36) // CALLDATASIZE
	b = append(b, 0x60, 0x00, 0x60, 0x00) // PUSH1 0（offset）、PUSH1 0（dest 頂）
	b = append(b, 0x37) // CALLDATACOPY
	// DELEGATECALL 參數（messageCallOp pop：gas, to, inOff, inSize, outOff, outSize；
	// 故 push 底→頂：outSize, outOff, inSize, inOff, to, gas）
	b = append(b, 0x60, 0x00)             // outSize
	b = append(b, 0x60, 0x00)             // outOff
	b = append(b, 0x36)                   // inSize = CALLDATASIZE
	b = append(b, 0x60, 0x00)             // inOff
	b = append(b, push32(proxyImplSlot)...) // to = storage[implSlot]
	b = append(b, 0x54) // SLOAD
	b = append(b, push32(proxyMask20)...)
	b = append(b, 0x16) // AND → to
	b = append(b, 0x5A) // gas
	b = append(b, 0xF4) // DELEGATECALL → [ok]
	b = append(b, 0x15)       // ISZERO（ok=0 → 跳 fail）
	b = append(b, 0x61, 0x00, 0x00, 0x57) // PUSH2 fail; JUMPI
	jumps = append(jumps, segJump{pos: len(b) - 3, target: 0})
	// success：複製 returndata 並 RETURN
	b = append(b, 0x3D)       // RETURNDATASIZE
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0x3E) // RETURNDATACOPY(0,0,size)
	b = append(b, 0x3D)       // RETURNDATASIZE
	b = append(b, 0x60, 0x00, 0xF3) // RETURN(0,size)
	failPos := len(b)
	b = append(b, 0x5B) // JUMPDEST
	b = append(b, 0x60, 0x00, 0x60, 0x00, 0xFD) // REVERT(0,0)
	return builtSeg{code: b, jumps: []segJump{{pos: jumps[0].pos, target: failPos}}}
}

// Calldata helpers（供 RPC/測試組裝交易）。

// ProxyImplCalldata 組裝 implementation() calldata。
func ProxyImplCalldata() []byte { return append([]byte{}, SelectorProxyImpl...) }

// ProxyAdminCalldata 組裝 admin() calldata。
func ProxyAdminCalldata() []byte { return append([]byte{}, SelectorProxyAdmin...) }

// ProxyUpgradeToCalldata 組裝 upgradeTo(address) calldata。
func ProxyUpgradeToCalldata(addr string) []byte {
	b := append([]byte{}, SelectorProxyUpgradeTo...)
	b = append(b, leftPad20(proxyMustHex(addr), 32)...)
	return b
}

// ProxyChangeAdminCalldata 組裝 changeAdmin(address) calldata。
func ProxyChangeAdminCalldata(addr string) []byte {
	b := append([]byte{}, SelectorProxyChangeAdmin...)
	b = append(b, leftPad20(proxyMustHex(addr), 32)...)
	return b
}

// leftPad20 把 20 字節地址左補 0 至 width 字節（EVM ABI 參數右對齊）。
func leftPad20(addr []byte, width int) []byte {
	out := make([]byte, width)
	copy(out[width-len(addr):], addr)
	return out
}

// proxyMustHex 解析 hex 字串（容忍 0x 前綴；供內嵌常數，panic 表示程式碼錯誤）。
func proxyMustHex(s string) []byte {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	out, err := hex.DecodeString(s)
	if err != nil {
		panic("proxy: 內嵌 hex 錯誤: " + err.Error())
	}
	return out
}

// proxyMustTopic 把 32 字節 hex 轉為 4-byte selector 數值（LOG topic push 用）。
func proxyMustTopic(s string) []byte {
	b := proxyMustHex(s)
	if len(b) != 32 {
		panic("proxy: topic 需 32 字節")
	}
	return b
}
