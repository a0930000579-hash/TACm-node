// tacctl 是 TAC Ai 智能鏈的輕量運維命令列：錢包生成、普通轉賬、
// 智能合約部署/調用與查詢。全部交易走節點 RPC（/tx/submit），真實 ECDSA 簽名。
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "wallet":
		err = cmdWallet()
	case "transfer":
		err = cmdTransfer(os.Args[2:])
	case "deploy":
		err = cmdDeploy(os.Args[2:])
	case "call":
		err = cmdCall(os.Args[2:])
	case "list":
		err = cmdList(os.Args[2:])
	case "get":
		err = cmdGet(os.Args[2:])
	case "storage":
		err = cmdStorage(os.Args[2:])
	case "audit":
		err = cmdAudit(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "錯誤:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`tacctl - TAC Ai 智能鏈運維工具
用法:
  tacctl wallet
  tacctl transfer -node URL -wif WIF -to ADDR -amount N [-fee N]
  tacctl deploy   -node URL -wif WIF -init HEX [-amount N] [-fee N]
  tacctl call     -node URL -wif WIF -contract ADDR [-calldata HEX] [-amount N] [-fee N]
  tacctl list     -node URL
  tacctl get      -node URL -contract ADDR
  tacctl audit    -node URL
`)
	os.Exit(0)
}

func cmdWallet() error {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}
	addr, err := kp.Address()
	if err != nil {
		return err
	}
	wif, err := crypto.PrivateKeyToWIF(kp.PrivateKey())
	if err != nil {
		return err
	}
	fmt.Println("地址:", addr)
	fmt.Println("WIF :", wif)
	return nil
}

type commonFlags struct {
	node, wif, to, contract, init, calldata, amount, fee, key string
}

func bindCommon(fs *flag.FlagSet, c *commonFlags, needContract bool) {
	fs.StringVar(&c.node, "node", "http://127.0.0.1:8332", "節點 RPC URL")
	fs.StringVar(&c.wif, "wif", "", "付款方 WIF 私鑰")
	fs.StringVar(&c.to, "to", "", "收款地址")
	fs.StringVar(&c.contract, "contract", "", "合約地址")
	fs.StringVar(&c.init, "init", "", "構造字節碼 hex")
	fs.StringVar(&c.calldata, "calldata", "", "calldata hex")
	fs.StringVar(&c.amount, "amount", "0", "金額")
	fs.StringVar(&c.fee, "fee", "1", "手續費")
	fs.StringVar(&c.key, "key", "0", "存儲槽 key")
}

func kpFromWIF(wif string) (*crypto.KeyPair, error) {
	wif = strings.TrimSpace(wif)
	if wif == "" {
		return nil, fmt.Errorf("缺少 -wif（可傳 WIF 或 64hex 私鑰）")
	}
	// 64 hex（可選 0x）視為原始私鑰。
	if raw := strip0x(wif); len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil {
			return crypto.KeyPairFromPrivateKey(b)
		}
	}
	return crypto.KeyPairFromWIF(wif)
}

// accountNonce 從節點取地址 nonce。
func accountNonce(node, addr string) (int64, error) {
	var acc struct {
		Nonce int64 `json:"nonce"`
	}
	if err := getJSON(node+"/account/"+addr, &acc); err != nil {
		return 0, err
	}
	return acc.Nonce, nil
}

func tx0To0x(addr string) (string, error) {
	h, err := crypto.AddressToHash160(addr)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(h), nil
}

func evmToTx0(evmAddr string) (string, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(evmAddr, "0x"))
	if err != nil {
		return "", err
	}
	return crypto.Hash160ToAddress(b), nil
}

// buildAndSubmit 構造簽名交易並提交。
func buildAndSubmit(node string, kp *crypto.KeyPair, to, amount, fee string,
	nonce int64, memo string) (string, error) {
	addr, err := kp.Address()
	if err != nil {
		return "", err
	}
	tx := map[string]any{
		"from": addr, "to": to, "amount": amount, "fee": fee,
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	if memo != "" {
		tx["memo"] = memo
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		return "", err
	}
	tx["signature"] = sig

	res, err := postJSON(node+"/tx/submit", tx)
	if err != nil {
		return "", err
	}
	h, _ := res["tx_hash"].(string)
	return h, nil
}

func cmdTransfer(args []string) error {
	fs := flag.NewFlagSet("transfer", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c, false)
	_ = fs.Parse(args)
	kp, err := kpFromWIF(c.wif)
	if err != nil {
		return err
	}
	if c.to == "" {
		return fmt.Errorf("缺少 -to")
	}
	addr, _ := kp.Address()
	nonce, err := accountNonce(c.node, addr)
	if err != nil {
		return err
	}
	h, err := buildAndSubmit(c.node, kp, c.to, c.amount, c.fee, nonce, "")
	if err != nil {
		return err
	}
	fmt.Println("已提交轉賬, tx:", h)
	return nil
}

func cmdDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c, false)
	_ = fs.Parse(args)
	kp, err := kpFromWIF(c.wif)
	if err != nil {
		return err
	}
	if c.init == "" {
		return fmt.Errorf("缺少 -init hex")
	}
	addr, _ := kp.Address()
	nonce, err := accountNonce(c.node, addr)
	if err != nil {
		return err
	}
	creator0x, err := tx0To0x(addr)
	if err != nil {
		return err
	}
	contract0x := vm.CreateAddress(creator0x, uint64(nonce))
	contractTx0, err := evmToTx0(contract0x)
	if err != nil {
		return err
	}
	memo := "vm:deploy:" + strip0x(c.init)
	h, err := buildAndSubmit(c.node, kp, contractTx0, c.amount, c.fee, nonce, memo)
	if err != nil {
		return err
	}
	fmt.Println("部署交易:", h)
	fmt.Println("合約地址:", contractTx0)
	return nil
}

func cmdCall(args []string) error {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c, true)
	_ = fs.Parse(args)
	kp, err := kpFromWIF(c.wif)
	if err != nil {
		return err
	}
	if c.contract == "" {
		return fmt.Errorf("缺少 -contract")
	}
	addr, _ := kp.Address()
	nonce, err := accountNonce(c.node, addr)
	if err != nil {
		return err
	}
	memo := "vm:call:" + strip0x(c.calldata)
	h, err := buildAndSubmit(c.node, kp, c.contract, c.amount, c.fee, nonce, memo)
	if err != nil {
		return err
	}
	fmt.Println("調用交易:", h)
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c, false)
	_ = fs.Parse(args)
	var out struct {
		Contracts []map[string]any `json:"contracts"`
		Count     int              `json:"count"`
	}
	if err := getJSON(c.node+"/contract/list", &out); err != nil {
		return err
	}
	for _, ct := range out.Contracts {
		fmt.Printf("%s  code_size=%v balance=%v\n",
			ct["address"], ct["code_size"], ct["balance"])
	}
	fmt.Println("合約數:", out.Count)
	return nil
}

func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c, true)
	_ = fs.Parse(args)
	if c.contract == "" {
		return fmt.Errorf("缺少 -contract")
	}
	var info map[string]any
	if err := getJSON(c.node+"/contract/get/"+c.contract, &info); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(b))
	return nil
}

func strip0x(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "0x") }

func cmdStorage(args []string) error {
	fs := flag.NewFlagSet("storage", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c, true)
	_ = fs.Parse(args)
	if c.contract == "" {
		return fmt.Errorf("缺少 -contract")
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := getJSON(c.node+"/contract/storage/"+c.contract+"/"+c.key, &out); err != nil {
		return err
	}
	fmt.Printf("slot[%s] = %s\n", c.key, out.Value)
	return nil
}

func getJSON(url string, v any) error {
	res, err := http.Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		return fmt.Errorf("GET %s: HTTP %d %s", url, res.StatusCode, data)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

func postJSON(url string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	res, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out := map[string]any{}
	data, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(data, &out)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST %s: HTTP %d %s", url, res.StatusCode, data)
	}
	return out, nil
}
