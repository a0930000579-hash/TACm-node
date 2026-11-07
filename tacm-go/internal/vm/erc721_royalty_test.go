package vm

import (
	"math/big"
	"strings"
	"testing"
)

// TestERC721RoyaltyInfoDefault 驗證未設定版稅時 royaltyInfo 回傳 (0, 0)。
func TestERC721RoyaltyInfoDefault(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	res := execErc721As(world, rt, creator, Erc721RoyaltyInfoCalldata(big.NewInt(1), big.NewInt(1000)))
	if !res.Success {
		t.Fatalf("royaltyInfo 執行失敗: %v", res.Err)
	}
	if len(res.ReturnData) != 64 {
		t.Fatalf("royaltyInfo 應回傳 64 bytes，得到 %d", len(res.ReturnData))
	}
	if BytesToInt(res.ReturnData[0:32]).Sign() != 0 {
		t.Fatalf("未設定版稅 receiver 應為 0，得到 %s", BytesToInt(res.ReturnData[0:32]).Text(10))
	}
	if BytesToInt(res.ReturnData[32:64]).Sign() != 0 {
		t.Fatalf("未設定版稅 amount 應為 0，得到 %s", BytesToInt(res.ReturnData[32:64]).Text(10))
	}
}

// TestERC721SetRoyaltyInfoAndQuery 驗證部署者可設定版稅，且 royaltyInfo
// 正確計算 salePrice*bps/10000。
func TestERC721SetRoyaltyInfoAndQuery(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	bob := "0x" + strings.Repeat("22", 20)
	// 部署者設定 receiver=bob、bps=500（5%）
	res := execErc721As(world, rt, creator, Erc721SetRoyaltyInfoCalldata(bob, big.NewInt(500)))
	if !res.Success {
		t.Fatalf("setRoyaltyInfo 執行失敗: %v", res.Err)
	}
	// royaltyInfo(1, 1000) → (bob, 1000*500/10000=50)
	got := execErc721As(world, rt, creator, Erc721RoyaltyInfoCalldata(big.NewInt(1), big.NewInt(1000)))
	if !got.Success {
		t.Fatalf("royaltyInfo 執行失敗: %v", got.Err)
	}
	if len(got.ReturnData) != 64 {
		t.Fatalf("royaltyInfo 應回傳 64 bytes，得到 %d", len(got.ReturnData))
	}
	recv := BytesToInt(got.ReturnData[0:32])
	if recv.Cmp(addrToInt(bob)) != 0 {
		t.Fatalf("receiver 應為 %s，得到 %s", addrToInt(bob).Text(16), recv.Text(16))
	}
	amt := BytesToInt(got.ReturnData[32:64])
	if amt.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("amount 應為 50（1000*500/10000），得到 %s", amt.Text(10))
	}
	// 另一價格驗證：5000*500/10000=250
	got2 := execErc721As(world, rt, creator, Erc721RoyaltyInfoCalldata(big.NewInt(7), big.NewInt(5000)))
	if !got2.Success {
		t.Fatalf("royaltyInfo 執行失敗: %v", got2.Err)
	}
	if amt2 := BytesToInt(got2.ReturnData[32:64]); amt2.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("amount 應為 250（5000*500/10000），得到 %s", amt2.Text(10))
	}
}

// TestERC721SetRoyaltyInfoOnlyDeployer 驗證非部署者呼叫 setRoyaltyInfo 被 REVERT。
func TestERC721SetRoyaltyInfoOnlyDeployer(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	bob := "0x" + strings.Repeat("22", 20)
	carol := "0x" + strings.Repeat("33", 20)
	if res := execErc721As(world, rt, bob, Erc721SetRoyaltyInfoCalldata(carol, big.NewInt(100))); res.Success {
		t.Fatal("非部署者 setRoyaltyInfo 應 REVERT，但成功")
	}
	// 部署者仍可設定（權限未被破壞）
	if res := execErc721As(world, rt, creator, Erc721SetRoyaltyInfoCalldata(bob, big.NewInt(100))); !res.Success {
		t.Fatalf("部署者 setRoyaltyInfo 應成功: %v", res.Err)
	}
}

// TestERC721SupportsInterface 驗證 supportsInterface 對
// ERC-165/721/721Metadata/2981 回傳 1，其他回傳 0。
func TestERC721SupportsInterface(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	for _, id := range [][]byte{Interface165, Interface721, Interface721Metadata, Interface2981} {
		if got := queryErc721(t, world, rt, creator, Erc721SupportsInterfaceCalldata(id)); got.Cmp(big.NewInt(1)) != 0 {
			t.Fatalf("supportsInterface(%x) 應為 1，得到 %s", id, got.Text(10))
		}
	}
	other := []byte{0xde, 0xad, 0xbe, 0xef}
	if got := queryErc721(t, world, rt, creator, Erc721SupportsInterfaceCalldata(other)); got.Sign() != 0 {
		t.Fatalf("supportsInterface(%x) 應為 0，得到 %s", other, got.Text(10))
	}
}
