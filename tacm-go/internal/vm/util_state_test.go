package vm

import (
	"encoding/hex"
	"math/big"
	"testing"
)

func TestKeccakVectors(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{nil, "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"},
		{[]byte("abc"), "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(Keccak256(c.in)); got != c.want {
			t.Errorf("keccak(%q)=%s want %s", c.in, got, c.want)
		}
	}
}

func TestU256SignedRoundTrip(t *testing.T) {
	neg := big.NewInt(-5)
	enc := FromSigned(neg)
	if enc.Cmp(big.NewInt(0)) < 0 {
		t.Fatal("FromSigned 應為無符號")
	}
	if back := ToSigned(enc); back.Cmp(neg) != 0 {
		t.Errorf("往返=%d 應為 -5", back)
	}
	// 溢出截斷。
	over := new(big.Int).Add(MaxUint256, big.NewInt(7))
	if got := U256(over); got.Cmp(big.NewInt(6)) != 0 {
		t.Errorf("U256(2^256+6)=%s 應為 6", got)
	}
}

func TestIntBytesRoundTrip(t *testing.T) {
	x := big.NewInt(0x123456)
	b := IntToBytes(x, 32)
	if len(b) != 32 {
		t.Fatal("應為 32 字節")
	}
	if BytesToInt(b).Cmp(x) != 0 {
		t.Error("字節往返不一致")
	}
}

func TestNormalizeAddress(t *testing.T) {
	if got := NormalizeAddress("0x12"); got != "0x0000000000000000000000000000000000000012" {
		t.Errorf("短地址規範=%s", got)
	}
	if got := NormalizeAddress(big.NewInt(1)); got != "0x0000000000000000000000000000000000000001" {
		t.Errorf("int 地址規範=%s", got)
	}
}

func TestWorldStateBasics(t *testing.T) {
	s := NewWorldState()
	a := "0x00000000000000000000000000000000000000aa"
	s.SetBalance(a, big.NewInt(100))
	s.SetCode(a, []byte{0x60, 0x00})
	s.SetStorage(a, big.NewInt(1), big.NewInt(42))
	s.IncNonce(a)

	if s.GetBalance(a).Cmp(big.NewInt(100)) != 0 {
		t.Error("餘額錯誤")
	}
	if !s.HasCode(a) {
		t.Error("應有代碼")
	}
	if s.GetStorage(a, big.NewInt(1)).Cmp(big.NewInt(42)) != 0 {
		t.Error("存儲錯誤")
	}
	if s.GetNonce(a) != 1 {
		t.Error("nonce 錯誤")
	}

	// 轉賬成功。
	b := "0x00000000000000000000000000000000000000bb"
	if !s.Transfer(a, b, big.NewInt(30)) {
		t.Fatal("轉賬應成功")
	}
	if s.GetBalance(b).Cmp(big.NewInt(30)) != 0 ||
		s.GetBalance(a).Cmp(big.NewInt(70)) != 0 {
		t.Error("轉賬後餘額錯誤")
	}
	// 餘額不足失敗。
	if s.Transfer(a, b, big.NewInt(1000)) {
		t.Error("餘額不足應失敗")
	}
}

func TestWorldStateSnapshotRevert(t *testing.T) {
	s := NewWorldState()
	a := "0x00000000000000000000000000000000000000aa"
	s.SetBalance(a, big.NewInt(100))
	snap := s.Snapshot()

	s.SetBalance(a, big.NewInt(999))
	s.SetStorage(a, big.NewInt(1), big.NewInt(1))
	s.Revert(snap)

	if s.GetBalance(a).Cmp(big.NewInt(100)) != 0 {
		t.Error("回滾後餘額應為 100")
	}
	if s.GetStorage(a, big.NewInt(1)).Sign() != 0 {
		t.Error("回滾後存儲應清空")
	}
}
