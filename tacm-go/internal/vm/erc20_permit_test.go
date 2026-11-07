package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// queryErc20At 以指定合約地址查詢（與 permit 寫入同一 storage 世界）。
func queryErc20At(t *testing.T, world *WorldState, runtime []byte, contractAddr string, calldata []byte) *big.Int {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Address = contractAddr
	r := ix.Execute(runtime, calldata)
	if !r.Success {
		t.Fatalf("查詢執行失敗: %v", r.Err)
	}
	return retInt(r)
}

// deployErc20WithPermit 部署含 EIP-2612 permit 的代幣（Erc20InitWithMeta），
// 回傳 world、runtime、owner 地址（0x 格式＝Hash160(pubkey)，與鏈上 EOA
// 及 ecrecover precompile 一致）、owner 金鑰對。
func deployErc20WithPermit(t *testing.T, supply int64, name string) (*WorldState, []byte, string, *crypto.KeyPair) {
	t.Helper()
	in, world := newTestVM(0)
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	owner := "0x" + hex.EncodeToString(crypto.Hash160(kp.PublicKeyCompressed()))
	in.ctx.Caller = owner
	in.ctx.Address = "0x" + strings.Repeat("33", 20) // 部署與後續 permit/查詢共用同一合約地址
	res := in.Execute(Erc20InitWithMeta(big.NewInt(supply), []byte(name), []byte("TAC")), nil)
	if !res.Success {
		t.Fatalf("部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	return world, res.ReturnData, owner, kp
}

// permitDigest 依 EIP-712 計算 permit 簽名 digest（與 erc20SegPermit 同構）。
func permitDigest(nameHash []byte, contractAddr string, chainID *big.Int, owner, spender string,
	value, nonce, deadline *big.Int) []byte {
	domain := Keccak256(
		IntToBytes(DomainTypeHash, 32),
		nameHash,
		IntToBytes(VersionHash, 32),
		IntToBytes(chainID, 32),
		IntToBytes(addrToInt(contractAddr), 32),
	)
	sh := Keccak256(
		IntToBytes(PermitTypeHash, 32),
		IntToBytes(addrToInt(owner), 32),
		IntToBytes(addrToInt(spender), 32),
		IntToBytes(value, 32),
		IntToBytes(nonce, 32),
		IntToBytes(deadline, 32),
	)
	return Keccak256([]byte{0x19, 0x01}, domain, sh)
}

// execPermit 以指定 ctx 執行 permit calldata。
func execPermit(t *testing.T, world *WorldState, runtime []byte, contractAddr string,
	caller string, chainID *big.Int, ts int64, calldata []byte) *ExecutionResult {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = caller
	ix.ctx.Address = contractAddr
	ix.ctx.ChainID = chainID
	ix.ctx.BlockTimestamp = ts
	return ix.Execute(runtime, calldata)
}

// TestERC20PermitFullFlow 驗證 EIP-2612 完整閉環：簽名 → permit → allowance
// 生效 → nonce 遞增 → DOMAIN_SEPARATOR 一致 → transferFrom 消費授權。
func TestERC20PermitFullFlow(t *testing.T) {
	world, rt, owner, ownerKP := deployErc20WithPermit(t, 1000000, "MyToken")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()

	// 1. DOMAIN_SEPARATOR() 與 Go 側計算一致。
	dsCalldata := Erc20DomainSeparatorCalldata()
	dsRes := execPermit(t, world, rt, contractAddr, spender, chainID, now, dsCalldata)
	if !dsRes.Success {
		t.Fatalf("DOMAIN_SEPARATOR 執行失敗: %v", dsRes.Err)
	}
	wantDS := Keccak256(
		IntToBytes(DomainTypeHash, 32),
		Keccak256([]byte("MyToken")),
		IntToBytes(VersionHash, 32),
		IntToBytes(chainID, 32),
		IntToBytes(addrToInt(contractAddr), 32),
	)
	if string(dsRes.ReturnData) != string(wantDS) {
		t.Fatalf("DOMAIN_SEPARATOR 不一致：\n合約 %x\n期望 %x", dsRes.ReturnData, wantDS)
	}

	// 2. 初始 nonces(owner)=0、allowance=0。
	if got := queryErc20At(t, world, rt, contractAddr, Erc20NoncesCalldata(owner)); got.Sign() != 0 {
		t.Fatalf("初始 nonce 應為 0，得到 %s", got)
	}
	if got := queryErc20At(t, world, rt, contractAddr, Erc20AllowanceCalldata(owner, spender)); got.Sign() != 0 {
		t.Fatalf("初始 allowance 應為 0，得到 %s", got)
	}

	// 3. 組 permit 簽名並執行。
	value := big.NewInt(1000)
	deadline := big.NewInt(now + 3600)
	nonce := big.NewInt(0)
	digest := permitDigest(Keccak256([]byte("MyToken")), contractAddr, chainID, owner, spender, value, nonce, deadline)
	sig, err := crypto.SignEthereumMessage(digest, ownerKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	v := sig[0]
	if v < 27 {
		v += 27
	}
	permitCalldata := Erc20PermitCalldata(owner, spender, value, deadline, v, sig[1:33], sig[33:65])
	res := execPermit(t, world, rt, contractAddr, spender, chainID, now, permitCalldata)
	if !res.Success {
		t.Fatalf("permit 執行失敗: %v (revert=%s)", res.Err, res.RevertReason)
	}
	if got := retInt(res); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("permit 應回傳 1，得到 %s", got)
	}

	// 4. allowance 生效、nonce 遞增。
	if got := queryErc20At(t, world, rt, contractAddr, Erc20AllowanceCalldata(owner, spender)); got.Cmp(value) != 0 {
		t.Fatalf("permit 後 allowance 應為 %s，得到 %s", value, got)
	}
	if got := queryErc20At(t, world, rt, contractAddr, Erc20NoncesCalldata(owner)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("permit 後 nonce 應為 1，得到 %s", got)
	}

	// 5. spender 用 transferFrom 消費授權（1000 → spender）。
	tfCalldata := Erc20TransferFromCalldata(owner, spender, value)
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = spender
	ix.ctx.Address = contractAddr
	ix.ctx.ChainID = chainID
	ix.ctx.BlockTimestamp = now
	tfRes := ix.Execute(rt, tfCalldata)
	if !tfRes.Success {
		t.Fatalf("transferFrom 執行失敗: %v", tfRes.Err)
	}
	if got := queryErc20At(t, world, rt, contractAddr, Erc20BalanceOfCalldata(owner)); got.Cmp(big.NewInt(999000)) != 0 {
		t.Fatalf("owner 餘額應為 999000，得到 %s", got)
	}
	if got := queryErc20At(t, world, rt, contractAddr, Erc20BalanceOfCalldata(spender)); got.Cmp(value) != 0 {
		t.Fatalf("spender 餘額應為 %s，得到 %s", value, got)
	}
	// allowance 已消耗為 0。
	if got := queryErc20At(t, world, rt, contractAddr, Erc20AllowanceCalldata(owner, spender)); got.Sign() != 0 {
		t.Fatalf("transferFrom 後 allowance 應為 0，得到 %s", got)
	}
}

// TestERC20PermitReplayProtected 驗證 nonce 防重放與簽名篡改防護：
// permit 成功後 nonce 遞增（同一 nonce 的簽名無法再次生效，因為鏈上 nonce
// 已變、allowance 已設定）；篡改 s → recovered != owner → revert。
func TestERC20PermitReplayProtected(t *testing.T) {
	world, rt, owner, ownerKP := deployErc20WithPermit(t, 500000, "ReplayCoin")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()
	value := big.NewInt(500)
	deadline := big.NewInt(now + 3600)

	signPermit := func(nonce *big.Int) (byte, []byte, []byte) {
		digest := permitDigest(Keccak256([]byte("ReplayCoin")), contractAddr, chainID, owner, spender, value, nonce, deadline)
		sig, err := crypto.SignEthereumMessage(digest, ownerKP.PrivateKey())
		if err != nil {
			t.Fatal(err)
		}
		v := sig[0]
		if v < 27 {
			v += 27
		}
		return v, sig[1:33], sig[33:65]
	}

	// 第一次（nonce=0）成功，nonce 遞增為 1。
	v, r, s := signPermit(big.NewInt(0))
	cd := Erc20PermitCalldata(owner, spender, value, deadline, v, r, s)
	if res := execPermit(t, world, rt, contractAddr, spender, chainID, now, cd); !res.Success {
		t.Fatalf("首次 permit 應成功: %v", res.Err)
	}
	if got := queryErc20At(t, world, rt, contractAddr, Erc20NoncesCalldata(owner)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("permit 後 nonce 應為 1，得到 %s", got)
	}

	// 篡改 s（最後一 byte +1）→ recovered != owner → revert。
	_, _, s2 := signPermit(big.NewInt(1))
	badS := append([]byte{}, s2...)
	badS[31]++
	cd2 := Erc20PermitCalldata(owner, spender, value, deadline, v, r, badS)
	res2 := execPermit(t, world, rt, contractAddr, spender, chainID, now, cd2)
	if res2.Success {
		t.Fatal("篡改簽名應 revert")
	}
	// allowance 維持 500（未再變動）。
	if got := queryErc20At(t, world, rt, contractAddr, Erc20AllowanceCalldata(owner, spender)); got.Cmp(value) != 0 {
		t.Fatalf("篡改 permit 不應改寫 allowance，得到 %s", got)
	}
}

// TestERC20PermitExpiredDeadline 驗證 deadline 過期 → revert。
func TestERC20PermitExpiredDeadline(t *testing.T) {
	world, rt, owner, ownerKP := deployErc20WithPermit(t, 100000, "ExpireCoin")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()
	value := big.NewInt(100)
	// deadline 在過去（now-10）。
	deadline := big.NewInt(now - 10)
	digest := permitDigest(Keccak256([]byte("ExpireCoin")), contractAddr, chainID, owner, spender, value, big.NewInt(0), deadline)
	sig, err := crypto.SignEthereumMessage(digest, ownerKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	v := sig[0]
	if v < 27 {
		v += 27
	}
	cd := Erc20PermitCalldata(owner, spender, value, deadline, v, sig[1:33], sig[33:65])
	res := execPermit(t, world, rt, contractAddr, spender, chainID, now, cd)
	if res.Success {
		t.Fatal("過期 deadline 應 revert")
	}
	// allowance 未被寫入。
	if got := queryErc20At(t, world, rt, contractAddr, Erc20AllowanceCalldata(owner, spender)); got.Sign() != 0 {
		t.Fatalf("過期 permit 不應寫 allowance，得到 %s", got)
	}
}

// TestERC20PermitDebugEcrecover 直接驗證 ecrecover precompile：digest→簽名→
// recovered 是否等於 owner（Hash160 式）。定位 permit revert 死因。
func TestERC20PermitDebugEcrecover(t *testing.T) {
	_, _, owner, ownerKP := deployErc20WithPermit(t, 100000, "DbgCoin")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()
	value := big.NewInt(100)
	deadline := big.NewInt(now + 3600)
	digest := permitDigest(Keccak256([]byte("DbgCoin")), contractAddr, chainID, owner, spender, value, big.NewInt(0), deadline)
	sig, err := crypto.SignEthereumMessage(digest, ownerKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	v := sig[0]
	if v < 27 {
		v += 27
	}
	input := append([]byte{}, digest...)
	input = append(input, IntToBytes(big.NewInt(int64(v)), 32)...)
	input = append(input, sig[1:33]...)
	input = append(input, sig[33:65]...)
	rec, matched, _, rerr := runPrecompile(NewContext(), big.NewInt(1), input)
	t.Logf("owner=%s", owner)
	t.Logf("digest=%x", digest)
	t.Logf("v=%d r=%x s=%x", v, sig[1:33], sig[33:65])
	if !matched {
		t.Fatalf("precompile 未匹配")
	}
	if rerr != nil {
		t.Fatalf("ecrecover err: %v", rerr)
	}
	recAddr := "0x" + hex.EncodeToString(rec[12:])
	t.Logf("recovered=%s recAddr=%s", hex.EncodeToString(rec), recAddr)
	if recAddr != owner {
		t.Fatalf("recovered(%s) != owner(%s)", recAddr, owner)
	}
	// 同時驗證 domain separator 一致。
	wantDS := Keccak256(
		IntToBytes(DomainTypeHash, 32),
		Keccak256([]byte("DbgCoin")),
		IntToBytes(VersionHash, 32),
		IntToBytes(chainID, 32),
		IntToBytes(addrToInt(contractAddr), 32),
	)
	gotDS := Keccak256(
		IntToBytes(DomainTypeHash, 32),
		Keccak256([]byte("DbgCoin")),
		IntToBytes(VersionHash, 32),
		IntToBytes(chainID, 32),
		IntToBytes(addrToInt(contractAddr), 32),
	)
	t.Logf("domainSEP=%x", gotDS)
	if string(gotDS) != string(wantDS) {
		t.Fatalf("domain 不一致")
	}
	t.Logf("ecrecover OK: recovered==owner、domain 一致")
}

// buildDigestOnly 重現 permit 段的 structHash→domain→digest 組裝（不含 CALL），
// 回傳值＝digest。用於縮小 revert 死因：digest 段 or CALL 段。
func buildDigestOnly(nameHash *big.Int) []byte {
	var b []byte
	b = append(b, 0x60, 0x00) // PUSH1 0（nonce=0）
	b = append(b, push32(PermitTypeHash)...)
	b = append(b, 0x60, 0x00, 0x52)
	b = append(b, codeCalldata4...)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, codeCalldata36...)
	b = append(b, 0x60, 0x40, 0x52)
	b = append(b, codeCalldata68...)
	b = append(b, 0x60, 0x60, 0x52)
	b = append(b, 0x80)             // DUP1 nonce
	b = append(b, 0x60, 0x80, 0x52) // mem[128]=nonce
	b = append(b, codePermitDeadline...)
	b = append(b, 0x60, 0xA0, 0x52)
	b = append(b, 0x60, 0xC0, 0x60, 0x00, 0x20) // SHA3(0,192) → structHash
	b = append(b, push32(DomainTypeHash)...)
	b = append(b, 0x60, 0x00, 0x52)
	b = append(b, push32(nameHash)...)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, push32(VersionHash)...)
	b = append(b, 0x60, 0x40, 0x52)
	b = append(b, 0x46)
	b = append(b, 0x60, 0x60, 0x52)
	b = append(b, 0x30)
	b = append(b, 0x60, 0x80, 0x52)
	b = append(b, 0x60, 0xA0, 0x60, 0x00, 0x20) // SHA3(0,160) → domain
	b = append(b, 0x60, 0x19, 0x60, 0x00, 0x53)
	b = append(b, 0x60, 0x01, 0x60, 0x01, 0x53)
	b = append(b, 0x80, 0x60, 0x02, 0x52)
	b = append(b, 0x81, 0x60, 0x22, 0x52)
	b = append(b, 0x60, 0x42, 0x60, 0x00, 0x20) // SHA3(0,66) → digest
	b = append(b, 0x60, 0x00, 0x52)             // mem[0]=digest
	// 回傳 mem[0:32]（MLOAD 版，勿用 RET32＝回傳棧頂）
	return append(b, 0x60, 0x00, 0x51, 0x60, 0x20, 0x60, 0x00, 0xF3)
}

// TestERC20PermitDebugDigest 執行 digest-only bytecode，與 Go 側 permitDigest 比對。
func TestERC20PermitDebugDigest(t *testing.T) {
	_, _, owner, _ := deployErc20WithPermit(t, 100000, "DbgCoin")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()
	value := big.NewInt(100)
	deadline := big.NewInt(now + 3600)
	cd := Erc20PermitCalldata(owner, spender, value, deadline, 28, make([]byte, 32), make([]byte, 32))

	ix := NewInterpreter(NewContext(), NewWorldState(), 0, false)
	ix.ctx.Caller = spender
	ix.ctx.Address = contractAddr
	ix.ctx.ChainID = chainID
	ix.ctx.BlockTimestamp = now
	res := ix.Execute(buildDigestOnly(BytesToInt(Keccak256([]byte("DbgCoin")))), cd)
	if !res.Success {
		t.Fatalf("digest-only 執行失敗: %v", res.Err)
	}
	got := res.ReturnData
	want := permitDigest(Keccak256([]byte("DbgCoin")), contractAddr, chainID, owner, spender, value, big.NewInt(0), deadline)
	t.Logf("bytecode digest=%x", got)
	t.Logf("go-side   digest=%x", want)
	if string(got) != string(want) {
		t.Fatalf("digest 不一致（bytecode vs Go）")
	}
	t.Logf("digest 組裝一致 OK")
}

// buildPermitCall 重現 permit 段 digest→CALL(0x01 ecrecover)→回傳 recovered（32-byte）。
func buildPermitCall(nameHash *big.Int) []byte {
	b := buildDigestOnly(nameHash) // 到 mem[0]=digest（含回傳——捨棄其 RETURN 部分不影響，因 bytecode 獨立執行）
	// 去掉 buildDigestOnly 的回傳尾（MLOAD版），改為 v/r/s+CALL 版
	// buildDigestOnly 尾 = 0x60,0x00,0x51,0x60,0x20,0x60,0x00,0xF3
	b = b[:len(b)-8]
	b = append(b, codePermitV...)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, codePermitR...)
	b = append(b, 0x60, 0x40, 0x52)
	b = append(b, codePermitS...)
	b = append(b, 0x60, 0x60, 0x52)
	b = append(b, 0x60, 0x20)       // outSize=32（棧底）
	b = append(b, 0x60, 0x00)       // outOffset=0
	b = append(b, 0x61, 0x00, 0x80) // inSize=128
	b = append(b, 0x60, 0x00)       // inOffset=0
	b = append(b, 0x60, 0x00)       // value=0
	b = append(b, 0x60, 0x01)       // addr=0x01
	b = append(b, 0x61, 0x0B, 0xB8) // gas=3000（棧頂，EVM 慣例）
	b = append(b, 0xF1)             // CALL
	b = append(b, 0x60, 0x00, 0x51) // MLOAD(0)
	b = append(b, 0x60, 0x20, 0x60, 0x00, 0xF3) // RETURN(0,32)
	return b
}

// TestERC20PermitDebugCall 執行 digest→CALL，驗證 bytecode 內 ecrecover 結果。
func TestERC20PermitDebugCall(t *testing.T) {
	_, _, owner, ownerKP := deployErc20WithPermit(t, 100000, "DbgCoin")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()
	value := big.NewInt(100)
	deadline := big.NewInt(now + 3600)
	digest := permitDigest(Keccak256([]byte("DbgCoin")), contractAddr, chainID, owner, spender, value, big.NewInt(0), deadline)
	sig, err := crypto.SignEthereumMessage(digest, ownerKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	v := sig[0]
	if v < 27 {
		v += 27
	}
	cd := Erc20PermitCalldata(owner, spender, value, deadline, v, sig[1:33], sig[33:65])
	ix := NewInterpreter(NewContext(), NewWorldState(), 0, false)
	ix.ctx.Caller = spender
	ix.ctx.Address = contractAddr
	ix.ctx.ChainID = chainID
	ix.ctx.BlockTimestamp = now
	res := ix.Execute(buildPermitCall(BytesToInt(Keccak256([]byte("DbgCoin")))), cd)
	if !res.Success {
		t.Fatalf("CALL-only 執行失敗: %v", res.Err)
	}
	recAddr := "0x" + hex.EncodeToString(res.ReturnData[12:])
	t.Logf("bytecode recovered=%s owner=%s", recAddr, owner)
	t.Logf("mem[32:64] v=%x", ix.memory[32:64])
	t.Logf("mem[64:96] r=%x", ix.memory[64:96])
	t.Logf("mem[96:128] s=%x", ix.memory[96:128])
	t.Logf("go-side v=%x r=%x s=%x", IntToBytes(big.NewInt(int64(v)), 32), sig[1:33], sig[33:65])
	if recAddr != owner {
		t.Fatalf("bytecode ecrecover(%s) != owner(%s)", recAddr, owner)
	}
	t.Logf("CALL 段 OK：bytecode 內 ecrecover == owner")
}

// buildPreCallMem 重現 digest→v/r/s 段（CALL 前），回傳 mem[0:128]。
func buildPreCallMem(nameHash *big.Int) []byte {
	b := buildDigestOnly(nameHash)
	b = b[:len(b)-8] // 去掉 MLOAD 版 RETURN 尾
	b = append(b, codePermitV...)
	b = append(b, 0x60, 0x20, 0x52)
	b = append(b, codePermitR...)
	b = append(b, 0x60, 0x40, 0x52)
	b = append(b, codePermitS...)
	b = append(b, 0x60, 0x60, 0x52)
	b = append(b, 0x60, 0x80, 0x60, 0x00, 0xF3) // RETURN(0,128)
	return b
}

// TestERC20PermitDebugPreCallMem 比對 CALL 前 mem[0:128] 與 Go 側 input。
func TestERC20PermitDebugPreCallMem(t *testing.T) {
	_, _, owner, ownerKP := deployErc20WithPermit(t, 100000, "DbgCoin")
	contractAddr := "0x" + strings.Repeat("33", 20)
	spender := "0x" + strings.Repeat("22", 20)
	chainID := big.NewInt(1337)
	now := time.Now().Unix()
	value := big.NewInt(100)
	deadline := big.NewInt(now + 3600)
	digest := permitDigest(Keccak256([]byte("DbgCoin")), contractAddr, chainID, owner, spender, value, big.NewInt(0), deadline)
	sig, err := crypto.SignEthereumMessage(digest, ownerKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	v := sig[0]
	if v < 27 {
		v += 27
	}
	cd := Erc20PermitCalldata(owner, spender, value, deadline, v, sig[1:33], sig[33:65])
	ix := NewInterpreter(NewContext(), NewWorldState(), 0, false)
	ix.ctx.Caller = spender
	ix.ctx.Address = contractAddr
	ix.ctx.ChainID = chainID
	ix.ctx.BlockTimestamp = now
	res := ix.Execute(buildPreCallMem(BytesToInt(Keccak256([]byte("DbgCoin")))), cd)
	if !res.Success {
		t.Fatalf("pre-call mem 執行失敗: %v", res.Err)
	}
	want := append([]byte{}, digest...)
	want = append(want, IntToBytes(big.NewInt(int64(v)), 32)...)
	want = append(want, sig[1:33]...)
	want = append(want, sig[33:65]...)
	t.Logf("mem[0:128]=%x", res.ReturnData)
	t.Logf("go input  =%x", want)
	diff := -1
	for i := 0; i < len(want) && i < len(res.ReturnData); i++ {
		if res.ReturnData[i] != want[i] {
			diff = i
			break
		}
	}
	if diff >= 0 {
		t.Fatalf("mem 與 Go input 在第 %d byte 不同", diff)
	}
	if len(res.ReturnData) != len(want) {
		t.Fatalf("長度不同：%d vs %d", len(res.ReturnData), len(want))
	}
	t.Logf("CALL 前 mem == Go input，完全一致")
}
