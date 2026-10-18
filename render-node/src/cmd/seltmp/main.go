package main

import (
	"fmt"
	"tacm/internal/vm"
)

func main() {
	r := vm.TACWalletRuntime()
	sel := []byte{0x69, 0x4e, 0x80, 0xc3}
	fmt.Printf("runtime len=%d\n", len(r))
	for i := 0; i < len(r)-40; i++ {
		if r[i] == 0x7f && r[i+29] == sel[0] && r[i+30] == sel[1] && r[i+31] == sel[2] && r[i+32] == sel[3] && r[i+35] == 0x57 {
			dst := int(r[i+33])<<8 | int(r[i+34])
			fmt.Printf("match at i=%d (prev=0x%02x) seg at %d\n", i, r[i-1], dst)
		}
	}
}
