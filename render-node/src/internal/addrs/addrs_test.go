package addrs

import (
	"bytes"
	"strings"
	"testing"
)

func TestBlake2sVectors(t *testing.T) {
	cases := []struct {
		data []byte
		size int
		want string
	}{
		{[]byte("abc"), 20, "5ae3b99be29b01834c3b508521ede60438f8de17"},
		{[]byte("abc"), 24, "1e2ed10fcdbc46e0ab3ea3f268a6c288083ae04e3d63a8de"},
		{[]byte(""), 24, "a847d26c2f966c5c4cc222b174918a56037cdee34b3f872f"},
	}
	for _, c := range cases {
		got, err := SumBlake2s(c.data, c.size)
		if err != nil {
			t.Fatalf("blake2s(%q,%d) 報錯: %v", c.data, c.size, err)
		}
		if hex(got) != c.want {
			t.Errorf("blake2s(%q,%d)\n got=%s\nwant=%s", c.data, c.size, hex(got), c.want)
		}
	}
}

func TestBlake2sSizeError(t *testing.T) {
	if _, err := SumBlake2s([]byte("x"), 0); err == nil {
		t.Error("size=0 應報錯")
	}
	if _, err := SumBlake2s([]byte("x"), 33); err == nil {
		t.Error("size=33 應報錯")
	}
}

func TestTx0Address(t *testing.T) {
	pub := bytes.Repeat([]byte{2}, 32)

	v2, err := Tx0Address(pub, 2)
	if err != nil {
		t.Fatalf("v2 地址失敗: %v", err)
	}
	wantV2 := "tx0msnuispsn6qw4tcfgltm2chyutekutzluec4uzq"
	if v2 != wantV2 {
		t.Errorf("v2 地址不符\n got=%s\nwant=%s", v2, wantV2)
	}
	if len(v2) != len("tx0")+39 {
		t.Errorf("v2 總長應為42, got=%d", len(v2))
	}

	v1, err := Tx0Address(pub, 1)
	if err != nil {
		t.Fatalf("v1 地址失敗: %v", err)
	}
	wantV1 := "tx0wlehinh7yfihl2tsc3ig5rimx2re6erg"
	if v1 != wantV1 {
		t.Errorf("v1 地址不符\n got=%s\nwant=%s", v1, wantV1)
	}
}

func TestIsValidAddr(t *testing.T) {
	pub := bytes.Repeat([]byte{2}, 32)
	v2, _ := Tx0Address(pub, 2)
	v1, _ := Tx0Address(pub, 1)

	if !IsValidAddr(v2, false) {
		t.Error("v2 應有效（即使不接受v1）")
	}
	if IsValidAddr(v1, false) {
		t.Error("v1 在 acceptV1=false 時應無效")
	}
	if !IsValidAddr(v1, true) {
		t.Error("v1 在 acceptV1=true 時應有效")
	}
	// 大寫輸入：Python 端內部會轉小寫，因此同樣有效。
	if !IsValidAddr(strings.ToUpper(v2), true) {
		t.Errorf("大寫地址（內部轉小寫）應有效")
	}
	for _, bad := range []string{"", "tx0", "tacm1abc", "0x123"} {
		if IsValidAddr(bad, true) {
			t.Errorf("非法地址應無效: %q", bad)
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	pub := bytes.Repeat([]byte{2}, 32)
	v2, _ := Tx0Address(pub, 2)
	v1, _ := Tx0Address(pub, 1)

	// 已是 v2：返回自身。
	got, err := NormalizeAddr(v2)
	if err != nil {
		t.Fatalf("規範 v2 報錯: %v", err)
	}
	if got != v2 {
		t.Errorf("v2 規範化應返回自身\n got=%s\nwant=%s", got, v2)
	}

	// v1 → v2。
	got, err = NormalizeAddr(v1)
	if err != nil {
		t.Fatalf("v1 規範化報錯: %v", err)
	}
	want, _ := LegacyToTx0(v1, 2)
	if got != want {
		t.Errorf("v1→v2 不符\n got=%s\nwant=%s", got, want)
	}
	if !IsValidAddr(got, false) {
		t.Errorf("規範化結果應為合法 v2: %s", got)
	}

	// 舊前綴 tacm1 / apc1：應派生為合法 v2，不保留舊前綴。
	for _, legacy := range []string{"tacm1abcdef", "apc1zzzzzz"} {
		got, err := NormalizeAddr(legacy)
		if err != nil {
			t.Fatalf("舊前綴 %q 規範化報錯: %v", legacy, err)
		}
		if !strings.HasPrefix(got, "tx0") || !IsValidAddr(got, false) {
			t.Errorf("舊前綴 %q 應轉為 tx0 v2, got=%s", legacy, got)
		}
	}

	// 空字符串保持空。
	if got, _ := NormalizeAddr(""); got != "" {
		t.Errorf("空地址規範化應為空, got=%q", got)
	}
}

func hex(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = h[c>>4]
		out[2*i+1] = h[c&0xf]
	}
	return string(out)
}
