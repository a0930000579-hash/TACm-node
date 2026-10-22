package aimarket

// verify_test.go — 附加掛鉤 BuildVerifyRequest 純函式測試。

import (
	"strings"
	"testing"
)

func TestBuildVerifyRequest(t *testing.T) {
	m := Model{Id: 7, Owner: "0xowner", Name: "demo", Version: "3"}
	vr := BuildVerifyRequest("0xcaller", m, "hello", "world-out", 5)
	if vr.ModelId != 7 || vr.ModelVersion != "3" {
		t.Fatalf("model 欄位未帶上: %+v", vr)
	}
	if vr.SourceChain != VerifySourceChain {
		t.Fatalf("來源鏈應 %s, got %s", VerifySourceChain, vr.SourceChain)
	}
	if !strings.HasPrefix(vr.InputHash, "0x") || !strings.HasPrefix(vr.OutputHash, "0x") {
		t.Fatalf("hash 應為 0x hex: in=%s out=%s", vr.InputHash, vr.OutputHash)
	}
	if vr.InputHash == vr.OutputHash {
		t.Fatalf("input/output hash 應不同")
	}
	if vr.TaskFee != 5 {
		t.Fatalf("taskFee 應 5, got %v", vr.TaskFee)
	}
	// 同輸入應確定性。
	if HashData("hello") != vr.InputHash {
		t.Fatalf("HashData 不確定性")
	}
}
