package sandbox

import (
	"encoding/binary"
	"fmt"
)

// Filter says which system calls the seccomp filter handed to bwrap blocks.
// Everything else is allowed: bwrap and the kernel do the real isolation;
// this only closes two holes that mounts and namespaces can't.
type Filter struct {
	// TIOCSTI blocks ioctl TIOCSTI and TIOCLINUX: typing into the
	// terminal on kernels that still allow it (legacy_tiocsti isn't 0).
	TIOCSTI bool
	// Vsock blocks socket(AF_VSOCK): on WSL, the VM's sockets lead to the
	// Windows host, outside every namespace.
	Vsock bool
}

// Any reports whether a filter is needed at all.
func (f Filter) Any() bool { return f.TIOCSTI || f.Vsock }

// Classic BPF as used by seccomp.
const (
	bpfLdAbs  = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeq    = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJset   = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfJa     = 0x05 // BPF_JMP | BPF_JA
	bpfRet    = 0x06 // BPF_RET | BPF_K
	retAllow  = 0x7fff0000
	retErrno  = 0x00050000
	errEPERM  = 1
	errNOSYS  = 38
	errNOAFSP = 97 // EAFNOSUPPORT

	// struct seccomp_data offsets; the low 32 bits of each argument, on
	// little-endian machines (amd64 and arm64, the only ones supported).
	offNr   = 0
	offArch = 4
	offArg0 = 16
	offArg1 = 24

	tiocsti   = 0x5412
	tioclinux = 0x541c
	afVsock   = 40
	x32Bit    = 0x40000000
)

// abi is one system call ABI a process on this machine may use.
type abi struct {
	arch          uint32
	ioctl, socket uint32
}

type archInfo struct {
	native abi
	compat abi  // the 32-bit ABI the kernel also accepts
	x32    bool // syscall numbers with bit 30 are the x32 ABI
}

var arches = map[string]archInfo{
	"amd64": {native: abi{0xc000003e, 16, 41}, compat: abi{0x40000003, 54, 359}, x32: true},
	"arm64": {native: abi{0xc00000b7, 29, 198}, compat: abi{0x40000028, 54, 281}},
}

// SeccompSupported reports whether box can build a filter for goarch.
func SeccompSupported(goarch string) bool {
	_, ok := arches[goarch]
	return ok
}

type insn struct {
	code          uint16
	k             uint32
	label         string // this instruction's label
	ifTrue, else_ string // jump targets; "" means the next instruction
}

// SeccompProgram compiles f for goarch into the bytes bwrap's --seccomp
// reads: struct sock_filter entries in the machine's (little-endian) order.
//
// The filter checks the ABI first, so a 32-bit or x32 syscall can't slip
// past checks written for the native numbers, and compares only the low 32
// bits of arguments, as the kernel does (Flatpak's CVE-2019-10063 was a
// 64-bit compare that setting high bits bypassed). Unknown ABIs get ENOSYS.
func SeccompProgram(goarch string, f Filter) ([]byte, error) {
	a, ok := arches[goarch]
	if !ok {
		return nil, fmt.Errorf("no seccomp filter for %s", goarch)
	}
	ld := func(off uint32) insn { return insn{code: bpfLdAbs, k: off} }
	jeq := func(k uint32, t, e string) insn { return insn{code: bpfJeq, k: k, ifTrue: t, else_: e} }
	ret := func(label string, k uint32) insn { return insn{code: bpfRet, k: k, label: label} }

	p := []insn{ld(offArch), jeq(a.native.arch, "", "compat"), ld(offNr)}
	if a.x32 {
		p = append(p, insn{code: bpfJset, k: x32Bit, ifTrue: "nosys"})
	}
	p = append(p,
		jeq(a.native.ioctl, "ioctl", ""),
		jeq(a.native.socket, "socket", "allow"),
		insn{code: bpfJeq, k: a.compat.arch, else_: "nosys", label: "compat"},
		ld(offNr),
		jeq(a.compat.ioctl, "ioctl", ""),
		jeq(a.compat.socket, "socket", "allow"),
	)
	if f.TIOCSTI {
		p = append(p,
			insn{code: bpfLdAbs, k: offArg1, label: "ioctl"},
			jeq(tiocsti, "eperm", ""),
			jeq(tioclinux, "eperm", "allow"),
		)
	} else {
		p = append(p, insn{code: bpfJa, ifTrue: "allow", label: "ioctl"})
	}
	if f.Vsock {
		p = append(p,
			insn{code: bpfLdAbs, k: offArg0, label: "socket"},
			jeq(afVsock, "eafnosupport", "allow"),
		)
	} else {
		p = append(p, insn{code: bpfJa, ifTrue: "allow", label: "socket"})
	}
	p = append(p,
		ret("allow", retAllow),
		ret("eperm", retErrno|errEPERM),
		ret("eafnosupport", retErrno|errNOAFSP),
		ret("nosys", retErrno|errNOSYS),
	)
	return assemble(p)
}

func assemble(p []insn) ([]byte, error) {
	at := map[string]int{}
	for i, in := range p {
		if in.label != "" {
			at[in.label] = i
		}
	}
	jump := func(i int, label string) (uint32, error) {
		if label == "" {
			return 0, nil
		}
		t, ok := at[label]
		if !ok || t <= i || t-i-1 > 255 {
			return 0, fmt.Errorf("seccomp: bad jump to %q from %d", label, i)
		}
		return uint32(t - i - 1), nil
	}
	out := make([]byte, 0, 8*len(p))
	for i, in := range p {
		k := in.k
		var jt, jf uint32
		var err error
		switch in.code {
		case bpfJa:
			if k, err = jump(i, in.ifTrue); err != nil {
				return nil, err
			}
		case bpfJeq, bpfJset:
			if jt, err = jump(i, in.ifTrue); err != nil {
				return nil, err
			}
			if jf, err = jump(i, in.else_); err != nil {
				return nil, err
			}
		}
		out = binary.LittleEndian.AppendUint16(out, in.code)
		out = append(out, byte(jt), byte(jf))
		out = binary.LittleEndian.AppendUint32(out, k)
	}
	return out, nil
}
