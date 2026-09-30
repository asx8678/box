package sandbox

import (
	"encoding/binary"
	"testing"
)

// run interprets a compiled seccomp program against one system call, the
// way the kernel would: loads read struct seccomp_data in native order.
func run(t *testing.T, prog []byte, arch, nr uint32, args ...uint64) uint32 {
	t.Helper()
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data[0:], nr)
	binary.LittleEndian.PutUint32(data[4:], arch)
	for i, a := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], a)
	}
	var acc uint32
	for pc := 0; pc < len(prog)/8; pc++ {
		ins := prog[8*pc:]
		code := binary.LittleEndian.Uint16(ins)
		jt, jf := int(ins[2]), int(ins[3])
		k := binary.LittleEndian.Uint32(ins[4:])
		switch code {
		case bpfLdAbs:
			acc = binary.LittleEndian.Uint32(data[k:])
		case bpfJeq:
			if acc == k {
				pc += jt
			} else {
				pc += jf
			}
		case bpfJset:
			if acc&k != 0 {
				pc += jt
			} else {
				pc += jf
			}
		case bpfJa:
			pc += int(k)
		case bpfRet:
			return k
		default:
			t.Fatalf("unknown opcode %#x at %d", code, pc)
		}
	}
	t.Fatal("program ran off the end")
	return 0
}

func TestSeccompProgram(t *testing.T) {
	const (
		allow      = retAllow
		eperm      = retErrno | errEPERM
		noaf       = retErrno | errNOAFSP
		nosys      = retErrno | errNOSYS
		afInet     = 2
		tiocgwinsz = 0x5413
	)
	for goarch, a := range arches {
		both, err := SeccompProgram(goarch, Filter{TIOCSTI: true, Vsock: true})
		if err != nil {
			t.Fatal(err)
		}
		vsockOnly, err := SeccompProgram(goarch, Filter{Vsock: true})
		if err != nil {
			t.Fatal(err)
		}
		n, c := a.native, a.compat
		cases := []struct {
			name string
			prog []byte
			arch uint32
			nr   uint32
			args []uint64
			want uint32
		}{
			{"TIOCSTI", both, n.arch, n.ioctl, []uint64{0, tiocsti}, eperm},
			{"TIOCLINUX", both, n.arch, n.ioctl, []uint64{0, tioclinux}, eperm},
			{"TIOCSTI with high bits set", both, n.arch, n.ioctl, []uint64{0, 0xdead_0000_0000_0000 | tiocsti}, eperm},
			{"other ioctl", both, n.arch, n.ioctl, []uint64{0, tiocgwinsz}, allow},
			{"TIOCSTI allowed when the kernel blocks it", vsockOnly, n.arch, n.ioctl, []uint64{0, tiocsti}, allow},
			{"AF_VSOCK", both, n.arch, n.socket, []uint64{afVsock, 1}, noaf},
			{"AF_INET", both, n.arch, n.socket, []uint64{afInet, 1}, allow},
			{"other syscall", both, n.arch, 999, nil, allow},
			{"32-bit TIOCSTI", both, c.arch, c.ioctl, []uint64{0, tiocsti}, eperm},
			{"32-bit AF_VSOCK", both, c.arch, c.socket, []uint64{afVsock}, noaf},
			{"32-bit other", both, c.arch, 3, nil, allow},
			{"unknown ABI", both, 0x12345678, n.ioctl, []uint64{0, tiocsti}, nosys},
		}
		if a.x32 {
			cases = append(cases, struct {
				name string
				prog []byte
				arch uint32
				nr   uint32
				args []uint64
				want uint32
			}{"x32 ABI", both, n.arch, x32Bit | n.ioctl, []uint64{0, tiocsti}, nosys})
		}
		for _, tc := range cases {
			if got := run(t, tc.prog, tc.arch, tc.nr, tc.args...); got != tc.want {
				t.Errorf("%s/%s: got %#x, want %#x", goarch, tc.name, got, tc.want)
			}
		}
	}
	if _, err := SeccompProgram("riscv64", Filter{TIOCSTI: true}); err == nil {
		t.Error("an unsupported architecture should be refused")
	}
}
