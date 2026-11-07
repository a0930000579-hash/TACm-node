// Command example 是 tacclient SDK 的最小可跑範例：連節點、查狀態、
// 生成密鑰、轉帳。執行：go run ./sdk/tacclient/example -node http://127.0.0.1:8080
package main

import (
	"flag"
	"fmt"
	"os"

	"tacm/sdk/tacclient"
)

func main() {
	node := flag.String("node", "http://127.0.0.1:8080", "TAC 節點 RPC 地址")
	to := flag.String("to", "", "收款地址（預設生成新密鑰自轉）")
	amount := flag.String("amount", "1", "轉帳金額")
	fee := flag.String("fee", "0.01", "手續費")
	flag.Parse()

	c := tacclient.NewClient(*node)

	st, err := c.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "連接節點失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("節點 %s | chain_id=%s | 高度=%d | 共識=%s\n",
		st.NodeID, st.ChainID, st.BlockHeight, st.Consensus)

	key, err := tacclient.GenerateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成密鑰失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("新地址: %s\n私鑰(請保管): %s\n", key.Address(), key.PrivateKeyHex())

	dest := *to
	if dest == "" {
		dest = key.Address()
		fmt.Println("未指定 -to，示範自轉帳")
	}
	bal, err := c.Balance(key.Address())
	if err != nil {
		fmt.Fprintf(os.Stderr, "查餘額失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("地址餘額: %s\n", bal)

	hash, err := c.Transfer(key, dest, *amount, *fee, "sdk-example")
	if err != nil {
		fmt.Fprintf(os.Stderr, "轉帳失敗: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("已提交交易: %s（出塊後可 GET /tx/%s 查詢）\n", hash, hash)
}
