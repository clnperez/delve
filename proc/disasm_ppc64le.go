package proc

import (
	"debug/gosym"
	"encoding/binary"

	"github.com/minux/power64/power64asm"
)

var maxInstructionLength uint64 = 15

type ArchInst power64asm.Inst

func asmDecode(mem []byte, pc uint64) (*ArchInst, error) {
	inst, err := power64asm.Decode(mem, binary.LittleEndian)
	if err != nil {
		return nil, err
	}
	patchPCPCRel(pc, &inst)
	r := ArchInst(inst)
	return &r, nil
}

func (inst *ArchInst) Size() int {
	return inst.Len
}

// converts PC relative arguments to absolute addresses
func patchPCPCRel(pc uint64, inst *power64asm.Inst) {
	for i := range inst.Args {
		rel, isrel := inst.Args[i].(power64asm.PCRel)
		if isrel {
			inst.Args[i] = power64asm.Imm(int64(pc) + int64(rel) + int64(inst.Len))
		}
	}
	return
}

func (inst *AsmInstruction) Text(flavour AssemblyFlavour) string {
	if inst.Inst == nil {
		return "?"
	}

	var text string

	switch flavour {
	case GNUFlavour:
		fallthrough
	default:
		text = power64asm.GNUSyntax(power64asm.Inst(*inst.Inst))
	}

	if inst.IsCall() && inst.DestLoc != nil && inst.DestLoc.Fn != nil {
		text += " " + inst.DestLoc.Fn.Name
	}

	return text
}

func (inst *AsmInstruction) IsCall() bool {
	return inst.Inst.Op == power64asm.SC
}

func (thread *Thread) resolveCallArg(inst *ArchInst, currentGoroutine bool, regs Registers) *Location {
	if inst.Op != power64asm.SC {
		return nil
	}

	var pc uint64
	var err error

	switch arg := inst.Args[0].(type) {
	case power64asm.Imm:
		pc = uint64(arg)
	case power64asm.Reg:
		fallthrough
	case power64asm.SpReg:
		fallthrough
	case power64asm.CondReg:
		if !currentGoroutine || regs == nil {
			return nil
		}
		pc, err = regs.Get(int(arg))
		if err != nil {
			return nil
		}
	case power64asm.Mem:
		if !currentGoroutine || regs == nil {
			return nil
		}
		if arg.Segment != 0 {
			return nil
		}
		regs, err := thread.Registers()
		if err != nil {
			return nil
		}
		base, err1 := regs.Get(int(arg.Base))
		index, err2 := regs.Get(int(arg.Index))
		if err1 != nil || err2 != nil {
			return nil
		}
		addr := uintptr(int64(base) + int64(index*uint64(arg.Scale)) + arg.Disp)
		//TODO: should this always be 64 bits instead of inst.MemBytes?
		pcbytes, err := thread.readMemory(addr, inst.MemBytes)
		if err != nil {
			return nil
		}
		pc = binary.LittleEndian.Uint64(pcbytes)
	case power64asm.PCRel:
	case power64asm.Label:
	case power64asm.Offset:
	default:
		return nil
	}

	file, line, fn := thread.dbp.PCToLine(pc)
	if fn == nil {
		return nil
	}
	return &Location{PC: pc, File: file, Line: line, Fn: fn}
}

type instrseq []power64asm.Op

var unixPrologue = instrseq{power64asm.LD, power64asm.ADDI, power64asm.CMPLD, power64asm.BLT, power64asm.MFLR, power64asm.BL}
var prologues = []instrseq{unixPrologue}

// FirstPCAfterPrologue returns the address of the first instruction after the prologue for function fn
// If sameline is set FirstPCAfterPrologue will always return an address associated with the same line as fn.Entry
func (dbp *Process) FirstPCAfterPrologue(fn *gosym.Func, sameline bool) (uint64, error) {
	text, err := dbp.CurrentThread.Disassemble(fn.Entry, fn.End, false)
	if err != nil {
		return fn.Entry, err
	}

	if len(text) <= 0 {
		return fn.Entry, nil
	}

	for _, prologue := range prologues {
		if len(prologue) >= len(text) {
			continue
		}
		if checkPrologue(text, prologue) {
			r := &text[len(prologue)]
			if sameline {
				if r.Loc.Line != text[0].Loc.Line {
					return fn.Entry, nil
				}
			}
			return r.Loc.PC, nil
		}
	}

	return fn.Entry, nil
}

func checkPrologue(s []AsmInstruction, prologuePattern instrseq) bool {
	for i, op := range prologuePattern {
		if s[i].Inst.Op != op {
			return false
		}
	}
	return true
}
