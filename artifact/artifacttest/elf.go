// Package artifacttest makes executables for tests of what reads them
// (artifact.CheckLibraries): the CLI's, and homeportd's build.
package artifacttest

import (
	"bytes"
	"encoding/binary"
)

// Exec is a little x86-64 ELF executable: its loader (PT_INTERP; ""
// for none, a static one), the libraries it needs (DT_NEEDED) and its
// RUNPATH ("" none) - all debug/elf reads of it.
func Exec(interp string, needed []string, runpath string) []byte {
	le := binary.LittleEndian
	// .dynstr, and the .dynamic that names its strings
	dynstr := []byte{0}
	str := func(s string) uint64 {
		off := len(dynstr)
		dynstr = append(append(dynstr, s...), 0)
		return uint64(off)
	}
	var dyn []byte
	tag := func(t, v uint64) { dyn = le.AppendUint64(le.AppendUint64(dyn, t), v) }
	for _, n := range needed {
		tag(1, str(n)) // DT_NEEDED
	}
	if runpath != "" {
		tag(29, str(runpath)) // DT_RUNPATH
	}
	tag(0, 0)
	shstr := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")

	const ehdr, phdr, shdr = 64, 56, 64
	nph := 0
	if interp != "" {
		nph = 1
	}
	off := ehdr + nph*phdr
	interpOff := off
	off += len(interp) + 1
	dynstrOff := off
	off += len(dynstr)
	dynOff := off
	off += len(dyn)
	shstrOff := off
	off += len(shstr)
	shoff := off

	var b bytes.Buffer
	h := make([]byte, ehdr)
	copy(h, "\x7fELF\x02\x01\x01")
	le.PutUint16(h[16:], 2)    // ET_EXEC
	le.PutUint16(h[18:], 0x3e) // x86-64
	le.PutUint32(h[20:], 1)
	if nph > 0 {
		le.PutUint64(h[32:], ehdr)
	}
	le.PutUint64(h[40:], uint64(shoff))
	le.PutUint16(h[52:], ehdr)
	le.PutUint16(h[54:], phdr)
	le.PutUint16(h[56:], uint16(nph))
	le.PutUint16(h[58:], shdr)
	le.PutUint16(h[60:], 4) // null, .dynstr, .dynamic, .shstrtab
	le.PutUint16(h[62:], 3)
	b.Write(h)
	if nph > 0 {
		p := make([]byte, phdr)
		le.PutUint32(p[0:], 3) // PT_INTERP
		le.PutUint32(p[4:], 4)
		le.PutUint64(p[8:], uint64(interpOff))
		le.PutUint64(p[32:], uint64(len(interp)+1))
		le.PutUint64(p[40:], uint64(len(interp)+1))
		le.PutUint64(p[48:], 1)
		b.Write(p)
	}
	b.WriteString(interp + "\x00")
	b.Write(dynstr)
	b.Write(dyn)
	b.Write(shstr)
	sec := func(name, typ uint32, off, size, link, entsize uint64) {
		s := make([]byte, shdr)
		le.PutUint32(s[0:], name)
		le.PutUint32(s[4:], typ)
		le.PutUint64(s[24:], off)
		le.PutUint64(s[32:], size)
		le.PutUint32(s[40:], uint32(link))
		le.PutUint64(s[48:], 1)
		le.PutUint64(s[56:], entsize)
		b.Write(s)
	}
	sec(0, 0, 0, 0, 0, 0)
	sec(1, 3, uint64(dynstrOff), uint64(len(dynstr)), 0, 0) // .dynstr, SHT_STRTAB
	sec(9, 6, uint64(dynOff), uint64(len(dyn)), 1, 16)      // .dynamic, SHT_DYNAMIC
	sec(18, 3, uint64(shstrOff), uint64(len(shstr)), 0, 0)  // .shstrtab
	return b.Bytes()
}
