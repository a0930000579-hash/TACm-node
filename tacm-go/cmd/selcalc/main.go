package main

import (
	"fmt"
	"tacm/internal/crypto"
)

func main() {
	sigs := []string{
		"propose(uint256,uint256)",
		"vote(uint256,uint256)",
		"execute(uint256)",
		"proposalInfo(uint256)",
	}
	for _, s := range sigs {
		h := crypto.Keccak256([]byte(s))
		fmt.Printf("%s -> %x\n", s, h[:4])
	}
}
