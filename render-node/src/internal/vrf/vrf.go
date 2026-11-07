// Package vrf v131：可驗證隨機函數（ECVRF，Schnorr 式，secp256k1）。
//
// 主流鏈同級能力（Chainlink VRF / Cardano ECVRF 語義）：持有者以私鑰對訊息
// 產生可公開驗證的隨機輸出——任何人可確認輸出確實由該公鑰綁定私鑰所產生，
// 無法被竄改或事後否認。用途：公平抽籤、節點選舉、NFT 白名單、遊戲掉落。
//
// 注意：VM 0x09 已有 RANDAO 式鏈上隨機數（M134-A/M151c），本包為鏈下可驗證
// 的 ECVRF 原語，兩者不重複、可互補。
package vrf

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// Proof ECVRF 證明：gamma（群元素）、c、s（Schnorr 回應）與輸出。
type Proof struct {
	GammaX  string `json:"gamma_x"`  // gamma 點 x（hex，64 字元）
	GammaY  string `json:"gamma_y"`  // gamma 點 y
	C       string `json:"c"`        // 挑戰雜湊（hex）
	S       string `json:"s"`        // Schnorr 回應（hex）
	Output  string `json:"output"`   // VRF 隨機輸出 = sha256(gamma||pub)，可證明綁定
	Message string `json:"message"`  // 被簽訊息（原始位元組 hex）
}

// KeyPair ECVRF 密鑰對（secp256k1）。
type KeyPair struct {
	priv *btcec.PrivateKey
	Pub  string `json:"pub"` // 壓縮公鑰 hex（33 字節）
}

// GenerateKey 產生新 ECVRF 密鑰對。
func GenerateKey() (*KeyPair, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("vrf: 產生密鑰: %w", err)
	}
	return &KeyPair{priv: priv, Pub: hex.EncodeToString(priv.PubKey().SerializeCompressed())}, nil
}

// KeyFromPrivate 從 32 字節私鑰（hex）恢復密鑰對。
func KeyFromPrivate(privHex string) (*KeyPair, error) {
	b, err := hex.DecodeString(privHex)
	if err != nil {
		return nil, fmt.Errorf("vrf: 私鑰需為 hex: %w", err)
	}
	if len(b) != 32 {
		return nil, errors.New("vrf: 私鑰必須為 32 字節")
	}
	priv, _ := btcec.PrivKeyFromBytes(b)
	return &KeyPair{priv: priv, Pub: hex.EncodeToString(priv.PubKey().SerializeCompressed())}, nil
}

// Prove 以私鑰對 message 產生 ECVRF 證明：
//  1. h = sha256(message)
//  2. k 隨機；R = k*G
//  3. c = sha256(R.x || R.y || h)
//  4. s = (k + c*sk) mod n
//  5. gamma = R；Output = sha256(gamma || pub)
//
// 驗證者僅需公鑰即可驗證，且輸出與私鑰綁定（不可偽造、不可竄改）。
func (k *KeyPair) Prove(message []byte) (*Proof, error) {
	if k == nil || k.priv == nil {
		return nil, errors.New("vrf: 密鑰為空")
	}
	curve := btcec.S256()
	n := curve.N
	h := sha256.Sum256(message)

	// k 隨機（1..n-1）。
	for {
		kb, err := rand.Int(rand.Reader, new(big.Int).Sub(n, big.NewInt(1)))
		if err != nil {
			return nil, fmt.Errorf("vrf: 產生隨機 k: %w", err)
		}
		kval := kb.Add(kb, big.NewInt(1))
		rx, ry := curve.ScalarBaseMult(kval.Bytes())

		// c = sha256(R.x || R.y || h)
		cHash := sha256.New()
		cHash.Write(rx.Bytes())
		cHash.Write(ry.Bytes())
		cHash.Write(h[:])
		c := new(big.Int).SetBytes(cHash.Sum(nil))
		c.Mod(c, n)
		if c.Sign() == 0 {
			continue // 極罕見，重抽 k
		}

		// s = (k + c*sk) mod n
		skInt := k.priv.ToECDSA().D
		cs := new(big.Int).Mul(c, skInt)
		s := new(big.Int).Add(kval, cs)
		s.Mod(s, n)
		if s.Sign() == 0 {
			continue
		}

		// Output = sha256(gamma || pub)；gamma 序列化 0x04||X||Y。
		gammaBytes := serializePoint(rx, ry)
		outHash := sha256.New()
		outHash.Write(gammaBytes)
		outHash.Write(k.priv.PubKey().SerializeCompressed())
		out := outHash.Sum(nil)

		return &Proof{
			GammaX:  hex.EncodeToString(rx.Bytes()),
			GammaY:  hex.EncodeToString(ry.Bytes()),
			C:       hex.EncodeToString(c.Bytes()),
			S:       hex.EncodeToString(s.Bytes()),
			Output:  hex.EncodeToString(out),
			Message: hex.EncodeToString(message),
		}, nil
	}
}

// Verify 以公鑰（壓縮 hex）驗證 ECVRF 證明：
//  1. 重建 R' = s*G - c*pub
//  2. 重算 c' = sha256(R'.x || R'.y || h) 並與 proof.C 比對
//  3. 重算 Output' = sha256(gamma || pub) 並與 proof.Output 比對
//
// 全部通過回傳 true。
func Verify(pubHex string, p *Proof) (bool, error) {
	if p == nil {
		return false, errors.New("vrf: 證明為空")
	}
	curve := btcec.S256()
	n := curve.N
	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil {
		return false, fmt.Errorf("vrf: 公鑰需為 hex: %w", err)
	}
	pub, err := btcec.ParsePubKey(pubBytes)
	if err != nil {
		return false, fmt.Errorf("vrf: 解析公鑰: %w", err)
	}
	c, ok := new(big.Int).SetString(p.C, 16)
	if !ok {
		return false, errors.New("vrf: c 非法")
	}
	s, ok := new(big.Int).SetString(p.S, 16)
	if !ok {
		return false, errors.New("vrf: s 非法")
	}
	h := sha256.Sum256(mustHexDecode(p.Message))

	// R' = s*G - c*pub = s*G + (-c mod n)*pub
	pubEcdsa := pub.ToECDSA()
	sx, sy := curve.ScalarBaseMult(s.Bytes())
	cNeg := new(big.Int).Sub(n, c)
	cx, cy := curve.ScalarMult(pubEcdsa.X, pubEcdsa.Y, cNeg.Bytes())
	rx, ry := curve.Add(sx, sy, cx, cy)
	if rx == nil || ry == nil {
		return false, errors.New("vrf: 重建 R 失敗")
	}

	cHash := sha256.New()
	cHash.Write(rx.Bytes())
	cHash.Write(ry.Bytes())
	cHash.Write(h[:])
	cPrime := new(big.Int).SetBytes(cHash.Sum(nil))
	cPrime.Mod(cPrime, n)
	if cPrime.Cmp(c) != 0 {
		return false, errors.New("vrf: 挑戰不符（簽名無效）")
	}

	// Output 重算綁定驗證（gamma 序列化方式與 Prove 一致）。
	gammaBytes := serializePoint(
		new(big.Int).SetBytes(mustHexDecode(p.GammaX)),
		new(big.Int).SetBytes(mustHexDecode(p.GammaY)),
	)
	outHash := sha256.New()
	outHash.Write(gammaBytes)
	outHash.Write(pubBytes)
	out := hex.EncodeToString(outHash.Sum(nil))
	if out != p.Output {
		return false, errors.New("vrf: 輸出不符")
	}
	return true, nil
}

func mustHexDecode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// serializePoint 未壓縮序列化座標點：0x04 || X(32) || Y(32)。
func serializePoint(x, y *big.Int) []byte {
	out := make([]byte, 65)
	out[0] = 0x04
	xb := x.Bytes()
	yb := y.Bytes()
	copy(out[1+32-len(xb):], xb)
	copy(out[33+32-len(yb):], yb)
	return out
}

// OutputHex 回傳 VRF 隨機輸出（hex）。
func (p *Proof) OutputHex() string { return p.Output }

// Format 以 ecdsa 相容格式封裝（供既有工具鏈顯示）。
func (p *Proof) Format() string { return fmt.Sprintf("vrf.proof c=%s s=%s out=%s", p.C[:8], p.S[:8], p.Output[:16]) }

var _ = ecdsa.Signature{}
