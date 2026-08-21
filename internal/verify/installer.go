package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/goflasher/goflasher/internal/gpt"
	"github.com/goflasher/goflasher/internal/installer"
)

var (
	ErrInstallerSemantic = errors.New("installer filesystem semantic verification failed")
	ErrGPTLayout         = errors.New("GPT layout verification failed")
	ErrFAT32Filesystem   = errors.New("FAT32 filesystem verification failed")
)

type InstallerOptions struct {
	SplitWIMPolicySize uint64
	RequireSplitWIM    bool
}
type InstallerResult struct {
	FilesVerified  int
	WIMParts       int
	ManifestSHA256 string
}

type fatVolume struct {
	r                                                                     io.ReaderAt
	base, size                                                            uint64
	bytesPerSector, sectorsPerCluster, reserved, fatSectors, totalSectors uint64
	fat                                                                   []byte
	owners                                                                map[uint32]string
	files                                                                 map[string]fatFile
}
type fatFile struct {
	path    string
	size    uint64
	cluster uint32
}

// VerifyInstaller rereads GPT, FAT metadata, directory trees, and file data
// exclusively from the raw target. It does not trust Builder state.
func VerifyInstaller(ctx context.Context, r io.ReaderAt, targetSize uint64, manifest []installer.VerificationEntry, options InstallerOptions) (InstallerResult, error) {
	var result InstallerResult
	if r == nil || targetSize < 512 || options.SplitWIMPolicySize == 0 {
		return result, semantic("invalid verifier input")
	}
	start, end, err := verifyGPT(r, targetSize)
	if err != nil {
		return result, errors.Join(ErrGPTLayout, err)
	}
	v, err := openFAT(r, start*512, (end-start+1)*512)
	if err != nil {
		return result, errors.Join(ErrFAT32Filesystem, err)
	}
	if err = v.walk(ctx); err != nil {
		return result, errors.Join(ErrFAT32Filesystem, err)
	}
	expected := make(map[string]installer.VerificationEntry, len(manifest))
	for _, e := range manifest {
		key := strings.ToLower(strings.ReplaceAll(e.Path, "\\", "/"))
		if _, ok := expected[key]; ok {
			return result, semantic("duplicate manifest path %s", e.Path)
		}
		expected[key] = e
	}
	for key, e := range expected {
		actual, ok := v.files[key]
		if !ok {
			return result, semantic("manifest path missing: %s", e.Path)
		}
		if actual.size != e.Size {
			return result, semantic("size mismatch for %s", e.Path)
		}
		h, err := v.hashFile(ctx, actual)
		if err != nil {
			return result, err
		}
		if h != e.SHA256 {
			return result, semantic("hash mismatch for %s", e.Path)
		}
		result.FilesVerified++
	}
	for _, required := range []string{"efi/boot/bootx64.efi", "sources/boot.wim"} {
		if _, ok := v.files[required]; !ok {
			return result, semantic("required installer file missing: %s", required)
		}
	}
	if options.RequireSplitWIM {
		if _, ok := v.files["sources/install.wim"]; ok {
			return result, semantic("oversized install.wim was copied")
		}
	}
	result.WIMParts, err = verifySWM(v.files, options)
	if err != nil {
		return result, err
	}
	result.ManifestSHA256 = manifestHash(manifest)
	return result, nil
}

func verifyGPT(r io.ReaderAt, size uint64) (uint64, uint64, error) {
	mbr, err := readAt(r, 0, 512)
	if err != nil {
		return 0, 0, err
	}
	if mbr[510] != 0x55 || mbr[511] != 0xaa || mbr[450] != 0xee {
		return 0, 0, semantic("invalid protective MBR")
	}
	total := size / 512
	if total < 68 {
		return 0, 0, semantic("target too small for GPT")
	}
	primary, err := readAt(r, 512, 512)
	if err != nil {
		return 0, 0, err
	}
	backup, err := readAt(r, (total-1)*512, 512)
	if err != nil {
		return 0, 0, err
	}
	if err = verifyGPTHeader(primary, 1, total-1); err != nil {
		return 0, 0, err
	}
	if err = verifyGPTHeader(backup, total-1, 1); err != nil {
		return 0, 0, err
	}
	count, sizeEntry := binary.LittleEndian.Uint32(primary[80:84]), binary.LittleEndian.Uint32(primary[84:88])
	if count == 0 || sizeEntry < 128 || uint64(count)*uint64(sizeEntry) > 64<<20 {
		return 0, 0, semantic("invalid GPT entry geometry")
	}
	bytesN := uint64(count) * uint64(sizeEntry)
	pa, err := readAt(r, binary.LittleEndian.Uint64(primary[72:80])*512, bytesN)
	if err != nil {
		return 0, 0, err
	}
	ba, err := readAt(r, binary.LittleEndian.Uint64(backup[72:80])*512, bytesN)
	if err != nil {
		return 0, 0, err
	}
	if crc32.ChecksumIEEE(pa) != binary.LittleEndian.Uint32(primary[88:92]) || crc32.ChecksumIEEE(ba) != binary.LittleEndian.Uint32(backup[88:92]) {
		return 0, 0, semantic("GPT partition array CRC mismatch")
	}
	if !bytes.Equal(pa, ba) {
		return 0, 0, semantic("primary and backup GPT arrays differ")
	}
	if !bytes.Equal(primary[56:72], backup[56:72]) || binary.LittleEndian.Uint64(primary[40:48]) != binary.LittleEndian.Uint64(backup[40:48]) || binary.LittleEndian.Uint64(primary[48:56]) != binary.LittleEndian.Uint64(backup[48:56]) {
		return 0, 0, semantic("primary and backup GPT metadata differ")
	}
	var found int
	var start, end uint64
	for off := uint64(0); off < bytesN; off += uint64(sizeEntry) {
		e := pa[off : off+uint64(sizeEntry)]
		if bytes.Equal(e[:16], make([]byte, 16)) {
			continue
		}
		found++
		if !bytes.Equal(e[:16], gpt.EFITypeGUID.MarshalBinary()) {
			return 0, 0, semantic("non-ESP GPT partition")
		}
		start, end = binary.LittleEndian.Uint64(e[32:40]), binary.LittleEndian.Uint64(e[40:48])
	}
	first, last := binary.LittleEndian.Uint64(primary[40:48]), binary.LittleEndian.Uint64(primary[48:56])
	if found != 1 || start < first || end > last || start > end {
		return 0, 0, semantic("invalid single ESP bounds")
	}
	return start, end, nil
}
func verifyGPTHeader(h []byte, current, alternate uint64) error {
	if string(h[:8]) != "EFI PART" || binary.LittleEndian.Uint64(h[24:32]) != current || binary.LittleEndian.Uint64(h[32:40]) != alternate {
		return semantic("invalid GPT header")
	}
	n := binary.LittleEndian.Uint32(h[12:16])
	if n < 92 || n > uint32(len(h)) {
		return semantic("invalid GPT header size")
	}
	copyH := append([]byte(nil), h[:n]...)
	want := binary.LittleEndian.Uint32(copyH[16:20])
	binary.LittleEndian.PutUint32(copyH[16:20], 0)
	if crc32.ChecksumIEEE(copyH) != want {
		return semantic("GPT header CRC mismatch")
	}
	return nil
}

func openFAT(r io.ReaderAt, base, size uint64) (*fatVolume, error) {
	boot, err := readAt(r, base, 512)
	if err != nil {
		return nil, err
	}
	if boot[510] != 0x55 || boot[511] != 0xaa || string(boot[82:90]) != "FAT32   " {
		return nil, semantic("invalid FAT32 boot sector")
	}
	bps := uint64(binary.LittleEndian.Uint16(boot[11:13]))
	spc := uint64(boot[13])
	reserved := uint64(binary.LittleEndian.Uint16(boot[14:16]))
	fats := uint64(boot[16])
	fatSectors := uint64(binary.LittleEndian.Uint32(boot[36:40]))
	total := uint64(binary.LittleEndian.Uint32(boot[32:36]))
	if bps != 512 || spc == 0 || spc&(spc-1) != 0 || reserved < 2 || fats != 2 || fatSectors == 0 || total*512 > size {
		return nil, semantic("invalid FAT32 BPB")
	}
	backupSector := uint64(binary.LittleEndian.Uint16(boot[50:52]))
	backup, err := readAt(r, base+backupSector*bps, bps)
	if err != nil || !bytes.Equal(boot, backup) {
		return nil, semantic("FAT32 backup boot sector mismatch")
	}
	fsi := uint64(binary.LittleEndian.Uint16(boot[48:50]))
	info, err := readAt(r, base+fsi*bps, bps)
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(info[:4]) != 0x41615252 || binary.LittleEndian.Uint32(info[484:488]) != 0x61417272 || binary.LittleEndian.Uint32(info[508:512]) != 0xaa550000 {
		return nil, semantic("invalid FAT32 FSInfo")
	}
	backupInfo, err := readAt(r, base+(backupSector+fsi)*bps, bps)
	if err != nil || !bytes.Equal(info, backupInfo) {
		return nil, semantic("FAT32 backup FSInfo mismatch")
	}
	fat1, err := readAt(r, base+reserved*bps, fatSectors*bps)
	if err != nil {
		return nil, err
	}
	fat2, err := readAt(r, base+(reserved+fatSectors)*bps, fatSectors*bps)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(fat1, fat2) {
		return nil, semantic("FAT mirrors differ")
	}
	return &fatVolume{r: r, base: base, size: size, bytesPerSector: bps, sectorsPerCluster: spc, reserved: reserved, fatSectors: fatSectors, totalSectors: total, fat: fat1, owners: map[uint32]string{}, files: map[string]fatFile{}}, nil
}
func (v *fatVolume) clusterSize() uint64 { return v.bytesPerSector * v.sectorsPerCluster }
func (v *fatVolume) maxCluster() uint32 {
	return uint32((v.totalSectors-v.reserved-2*v.fatSectors)/v.sectorsPerCluster + 1)
}
func (v *fatVolume) chain(first uint32, owner string) ([]uint32, error) {
	if first < 2 {
		return nil, nil
	}
	seen := map[uint32]bool{}
	var out []uint32
	for c := first; c < 0x0ffffff8; {
		if c < 2 || c > v.maxCluster() || int(c)*4+4 > len(v.fat) {
			return nil, semantic("cluster out of bounds for %s", owner)
		}
		if seen[c] {
			return nil, semantic("cluster loop for %s", owner)
		}
		if old, ok := v.owners[c]; ok && old != owner {
			return nil, semantic("cluster cross-link between %s and %s", old, owner)
		}
		seen[c] = true
		v.owners[c] = owner
		out = append(out, c)
		c = binary.LittleEndian.Uint32(v.fat[int(c)*4:int(c)*4+4]) & 0x0fffffff
	}
	return out, nil
}
func (v *fatVolume) cluster(c uint32) ([]byte, error) {
	off := v.base + (v.reserved+2*v.fatSectors+uint64(c-2)*v.sectorsPerCluster)*v.bytesPerSector
	return readAt(v.r, off, v.clusterSize())
}
func (v *fatVolume) walk(ctx context.Context) error { return v.walkDir(ctx, 2, "", map[uint32]bool{}) }
func (v *fatVolume) walkDir(ctx context.Context, first uint32, parent string, dirs map[uint32]bool) error {
	if dirs[first] {
		return semantic("directory cycle")
	}
	dirs[first] = true
	defer delete(dirs, first)
	chain, err := v.chain(first, "dir:"+parent)
	if err != nil {
		return err
	}
	var data []byte
	for _, c := range chain {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, e := v.cluster(c)
		if e != nil {
			return e
		}
		data = append(data, b...)
	}
	var lfn [][]byte
	for off := 0; off+32 <= len(data); off += 32 {
		e := data[off : off+32]
		if e[0] == 0 {
			break
		}
		if e[0] == 0xe5 {
			lfn = nil
			continue
		}
		if e[11] == 0x0f {
			lfn = append(lfn, append([]byte(nil), e...))
			continue
		}
		name := shortName(e)
		if len(lfn) > 0 {
			name = longName(lfn)
			lfn = nil
		}
		if name == "" || name == "." || name == ".." || e[11]&0x08 != 0 {
			continue
		}
		p := strings.ToLower(name)
		if parent != "" {
			p = parent + "/" + p
		}
		cluster := uint32(binary.LittleEndian.Uint16(e[26:28])) | uint32(binary.LittleEndian.Uint16(e[20:22]))<<16
		size := uint64(binary.LittleEndian.Uint32(e[28:32]))
		if e[11]&0x10 != 0 {
			if err := v.walkDir(ctx, cluster, p, dirs); err != nil {
				return err
			}
		} else {
			if _, exists := v.files[p]; exists {
				return semantic("duplicate FAT path %s", p)
			}
			v.files[p] = fatFile{p, size, cluster}
			if _, err := v.chain(cluster, "file:"+p); err != nil {
				return err
			}
		}
	}
	return nil
}
func shortName(e []byte) string {
	base := strings.TrimSpace(string(e[:8]))
	ext := strings.TrimSpace(string(e[8:11]))
	if ext != "" {
		return base + "." + ext
	}
	return base
}
func longName(es [][]byte) string {
	sort.Slice(es, func(i, j int) bool { return es[i][0]&0x1f < es[j][0]&0x1f })
	var u []uint16
	for _, e := range es {
		for _, x := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
			for i := x[0]; i < x[1]; i += 2 {
				c := binary.LittleEndian.Uint16(e[i : i+2])
				if c == 0 || c == 0xffff {
					break
				}
				u = append(u, c)
			}
		}
	}
	return string(utf16.Decode(u))
}
func (v *fatVolume) hashFile(ctx context.Context, f fatFile) (string, error) {
	chain, err := v.chain(f.cluster, "file:"+f.path)
	if err != nil {
		return "", err
	}
	needed := (f.size + v.clusterSize() - 1) / v.clusterSize()
	if uint64(len(chain)) != needed {
		return "", semantic("invalid chain length for %s", f.path)
	}
	h := sha256.New()
	remaining := f.size
	for _, c := range chain {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		b, e := v.cluster(c)
		if e != nil {
			return "", e
		}
		n := min(uint64(len(b)), remaining)
		h.Write(b[:n])
		remaining -= n
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func verifySWM(files map[string]fatFile, o InstallerOptions) (int, error) {
	parts := map[int]fatFile{}
	for p, f := range files {
		if p == "sources/install.swm" {
			parts[1] = f
			continue
		}
		if strings.HasPrefix(p, "sources/install") && strings.HasSuffix(p, ".swm") {
			var n int
			if _, err := fmt.Sscanf(p, "sources/install%d.swm", &n); err != nil || n < 2 || p != fmt.Sprintf("sources/install%d.swm", n) {
				return 0, semantic("invalid SWM name %s", p)
			}
			if _, ok := parts[n]; ok {
				return 0, semantic("duplicate SWM part")
			}
			parts[n] = f
		}
	}
	if o.RequireSplitWIM && len(parts) == 0 {
		return 0, semantic("split WIM set missing")
	}
	for i := 1; i <= len(parts); i++ {
		f, ok := parts[i]
		if !ok {
			return 0, semantic("non-contiguous SWM set")
		}
		if f.size == 0 || f.size > o.SplitWIMPolicySize {
			return 0, semantic("invalid SWM part size")
		}
	}
	return len(parts), nil
}
func manifestHash(entries []installer.VerificationEntry) string {
	c := append([]installer.VerificationEntry(nil), entries...)
	sort.Slice(c, func(i, j int) bool { return c[i].Path < c[j].Path })
	h := sha256.New()
	for _, e := range c {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", e.Path, e.Size, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func readAt(r io.ReaderAt, off, n uint64) ([]byte, error) {
	if off > uint64(^uint64(0)>>1) || n > 64<<20 {
		return nil, semantic("read out of bounds")
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, int64(off)); err != nil {
		return nil, err
	}
	return b, nil
}
func semantic(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInstallerSemantic, fmt.Sprintf(format, args...))
}
