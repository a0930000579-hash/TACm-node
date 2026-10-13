package vm

import "errors"

var (
	ErrStackOverflow  = errors.New("vm: stack overflow")
	ErrStackUnderflow = errors.New("vm: stack underflow")
	ErrOutOfGas       = errors.New("vm: out of gas")
	ErrInvalidJump    = errors.New("vm: invalid jump destination")
	ErrInvalidOpcode  = errors.New("vm: invalid opcode")
	ErrStaticState    = errors.New("vm: state modification in static context")
	ErrCallDepth      = errors.New("vm: call depth exceeded")
	ErrStepLimit      = errors.New("vm: execution step limit exceeded")
	ErrCodeTooLarge   = errors.New("vm: deployed code exceeds max size")
)
