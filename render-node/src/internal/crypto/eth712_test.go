package crypto

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

// EIP-712 官方文件 Mail 範例向量驗證。
// 向量來源：https://eips.ethereum.org/EIPS/eip-712（Example 段落）。
const mailTypedData = `{
  "types": {
    "EIP712Domain": [
      {"name": "name", "type": "string"},
      {"name": "version", "type": "string"},
      {"name": "chainId", "type": "uint256"},
      {"name": "verifyingContract", "type": "address"}
    ],
    "Person": [
      {"name": "name", "type": "string"},
      {"name": "wallet", "type": "address"}
    ],
    "Mail": [
      {"name": "from", "type": "Person"},
      {"name": "to", "type": "Person"},
      {"name": "contents", "type": "string"}
    ]
  },
  "primaryType": "Mail",
  "domain": {
    "name": "Ether Mail",
    "version": "1",
    "chainId": 1,
    "verifyingContract": "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
  },
  "message": {
    "from": {"name": "Cow", "wallet": "0xCD2a3d9F938E13CD947Ec05AbC7FE734Df8DD826"},
    "to": {"name": "Bob", "wallet": "0xbBbBBBBbbBBBbbbBbbBbbbbBBbBbbbbBbBbbBBbB"},
    "contents": "Hello, Bob!"
  }
}`

func TestEIP712OfficialVector(t *testing.T) {
	var td EIP712TypedData
	if err := json.Unmarshal([]byte(mailTypedData), &td); err != nil {
		t.Fatal(err)
	}

	// 1. encodeType("Mail") 字串。
	fields := td.Types["Mail"]
	enc, err := td.encodeType("Mail", fields)
	if err != nil {
		t.Fatal(err)
	}
	wantType := "Mail(Person from,Person to,string contents)Person(string name,address wallet)"
	if enc != wantType {
		t.Fatalf("encodeType 不符:\n got %q\nwant %q", enc, wantType)
	}

	// 2. domainSeparator 官方向量。
	ds, err := td.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	wantDS := "f2cee375fa42b42143804025fc449deafd50cc031ca257e0b194a650a912090f"
	if hex.EncodeToString(ds) != wantDS {
		t.Fatalf("domainSeparator 不符:\n got %x\nwant %s", ds, wantDS)
	}

	// 3. structHash 官方向量。
	sh, err := td.StructHash()
	if err != nil {
		t.Fatal(err)
	}
	wantSH := "c52c0ee5d84264471806290a3f2c4cecfc5490626bf912d01f240d7a274b371e"
	if hex.EncodeToString(sh) != wantSH {
		t.Fatalf("structHash 不符:\n got %x\nwant %s", sh, wantSH)
	}

	// 4. 最終簽名哈希：keccak256("" ‖ domainSeparator ‖ structHash)。
	// （ds/sh 已對官方向量；EIP-712 文件未直接給 final 數值，故以組合式自洽斷言。）
	fh, err := td.Hash()
	if err != nil {
		t.Fatal(err)
	}
	wantFH := Keccak256([]byte{0x19, 0x01}, ds, sh)
	if hex.EncodeToString(fh) != hex.EncodeToString(wantFH) {
		t.Fatalf("最終哈希 不符:\n got %x\nwant %x", fh, wantFH)
	}
}

func TestHashTypedDataV4JSON(t *testing.T) {
	fh, err := HashTypedDataV4([]byte(mailTypedData))
	if err != nil {
		t.Fatal(err)
	}
	var td EIP712TypedData
	if err := json.Unmarshal([]byte(mailTypedData), &td); err != nil {
		t.Fatal(err)
	}
	ds, _ := td.DomainSeparator()
	sh, _ := td.StructHash()
	want := Keccak256([]byte{0x19, 0x01}, ds, sh)
	if hex.EncodeToString(fh) != hex.EncodeToString(want) {
		t.Fatalf("HashTypedDataV4 不符: %x", fh)
	}
}

func TestEIP712InvalidInput(t *testing.T) {
	cases := []string{
		`{}`,
		`{"primaryType":"Mail","types":{}}`,
		`{"primaryType":"Mail","types":{"Mail":[]},"domain":{},"message":{}}`,
		`{"primaryType":"Missing","types":{"Mail":[]},"domain":{},"message":{}}`,
	}
	for i, c := range cases {
		if _, err := HashTypedDataV4([]byte(c)); err == nil {
			t.Fatalf("case %d 應報錯: %s", i, c)
		}
	}
}
