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

// RawTarget is the written device as the verifier sees it: a random-access
// reader and its total size in bytes.
type RawTarget struct {
	Reader io.ReaderAt
	Size   uint64
}

func (t RawTarget) valid() bool { return t.Reader != nil && t.Size >= 512 }

type fatVolume struct {
	fatGeometry
	r          io.ReaderAt
	base, size uint64
	fat        []byte
	owners     map[uint32]string
	files      map[string]fatFile
}
type fatFile struct {
	path    string
	size    uint64
	cluster uint32
}

var requiredInstallerFiles = []string{"efi/boot/bootx64.efi", "sources/boot.wim"}

// VerifyInstaller rereads GPT, FAT metadata, directory trees, and file data
// exclusively from the raw target. It does not trust Builder state.
func VerifyInstaller(ctx context.Context, target RawTarget, manifest []installer.VerificationEntry, options InstallerOptions) (InstallerResult, error) {
	var result InstallerResult
	if !target.valid() || options.SplitWIMPolicySize == 0 {
		return result, semantic("invalid verifier input")
	}
	v, err := openInstallerVolume(ctx, target)
	if err != nil {
		return result, err
	}
	expected, err := indexManifest(manifest)
	if err != nil {
		return result, err
	}
	if result.FilesVerified, err = v.verifyManifest(ctx, expected); err != nil {
		return result, err
	}
	if err = verifyInstallerLayout(v.files, options); err != nil {
		return result, err
	}
	if result.WIMParts, err = verifySWM(v.files, options); err != nil {
		return result, err
	}
	result.ManifestSHA256 = manifestHash(manifest)
	return result, nil
}

func openInstallerVolume(ctx context.Context, target RawTarget) (*fatVolume, error) {
	start, end, err := verifyGPT(target.Reader, target.Size)
	if err != nil {
		return nil, errors.Join(ErrGPTLayout, err)
	}
	v, err := openFAT(target.Reader, start*512, (end-start+1)*512)
	if err != nil {
		return nil, errors.Join(ErrFAT32Filesystem, err)
	}
	if err = v.walk(ctx); err != nil {
		return nil, errors.Join(ErrFAT32Filesystem, err)
	}
	return v, nil
}

func indexManifest(manifest []installer.VerificationEntry) (map[string]installer.VerificationEntry, error) {
	expected := make(map[string]installer.VerificationEntry, len(manifest))
	for _, e := range manifest {
		key := strings.ToLower(strings.ReplaceAll(e.Path, "\\", "/"))
		if _, ok := expected[key]; ok {
			return nil, semantic("duplicate manifest path %s", e.Path)
		}
		expected[key] = e
	}
	return expected, nil
}

func (v *fatVolume) verifyManifest(ctx context.Context, expected map[string]installer.VerificationEntry) (int, error) {
	verified := 0
	for key, e := range expected {
		if err := v.verifyEntry(ctx, key, e); err != nil {
			return verified, err
		}
		verified++
	}
	return verified, nil
}

func (v *fatVolume) verifyEntry(ctx context.Context, key string, e installer.VerificationEntry) error {
	actual, ok := v.files[key]
	if !ok {
		return semantic("manifest path missing: %s", e.Path)
	}
	if actual.size != e.Size {
		return semantic("size mismatch for %s", e.Path)
	}
	h, err := v.hashFile(ctx, actual)
	if err != nil {
		return err
	}
	if h != e.SHA256 {
		return semantic("hash mismatch for %s", e.Path)
	}
	return nil
}

func verifyInstallerLayout(files map[string]fatFile, options InstallerOptions) error {
	for _, required := range requiredInstallerFiles {
		if _, ok := files[required]; !ok {
			return semantic("required installer file missing: %s", required)
		}
	}
	if _, ok := files["sources/install.wim"]; ok && options.RequireSplitWIM {
		return semantic("oversized install.wim was copied")
	}
	return nil
}

// gptHeaders holds the primary and backup header sectors, which verifyGPT
// validates independently before comparing them with each other.
type gptHeaders struct {
	primary, backup []byte
	totalSectors    uint64
}

func verifyGPT(r io.ReaderAt, size uint64) (uint64, uint64, error) {
	if err := verifyProtectiveMBR(r); err != nil {
		return 0, 0, err
	}
	h, err := readGPTHeaders(r, size)
	if err != nil {
		return 0, 0, err
	}
	entries, entrySize, err := readGPTArray(r, h)
	if err != nil {
		return 0, 0, err
	}
	return findSingleESP(h.primary, entries, entrySize)
}

func verifyProtectiveMBR(r io.ReaderAt) error {
	mbr, err := readAt(r, 0, 512)
	if err != nil {
		return err
	}
	if !validProtectiveMBR(mbr) {
		return semantic("invalid protective MBR")
	}
	return nil
}

func validProtectiveMBR(mbr []byte) bool {
	return hasBootSignature(mbr) && mbr[450] == 0xee
}

func hasBootSignature(sector []byte) bool {
	return sector[510] == 0x55 && sector[511] == 0xaa
}

func readGPTHeaders(r io.ReaderAt, size uint64) (gptHeaders, error) {
	total := size / 512
	if total < 68 {
		return gptHeaders{}, semantic("target too small for GPT")
	}
	primary, err := readAt(r, 512, 512)
	if err != nil {
		return gptHeaders{}, err
	}
	backup, err := readAt(r, (total-1)*512, 512)
	if err != nil {
		return gptHeaders{}, err
	}
	if err = verifyGPTHeader(primary, 1, total-1); err != nil {
		return gptHeaders{}, err
	}
	if err = verifyGPTHeader(backup, total-1, 1); err != nil {
		return gptHeaders{}, err
	}
	if !sameGPTMetadata(primary, backup) {
		return gptHeaders{}, semantic("primary and backup GPT metadata differ")
	}
	return gptHeaders{primary: primary, backup: backup, totalSectors: total}, nil
}

// sameGPTMetadata compares the disk GUID and the usable LBA range, which must
// agree between the primary and backup headers.
func sameGPTMetadata(primary, backup []byte) bool {
	if !bytes.Equal(primary[56:72], backup[56:72]) {
		return false
	}
	return bytes.Equal(primary[40:56], backup[40:56])
}

func readGPTArray(r io.ReaderAt, h gptHeaders) ([]byte, uint64, error) {
	count, entrySize := binary.LittleEndian.Uint32(h.primary[80:84]), binary.LittleEndian.Uint32(h.primary[84:88])
	if !validGPTEntryGeometry(count, entrySize) {
		return nil, 0, semantic("invalid GPT entry geometry")
	}
	arrayBytes := uint64(count) * uint64(entrySize)
	primary, err := readVerifiedGPTArray(r, h.primary, arrayBytes)
	if err != nil {
		return nil, 0, err
	}
	backup, err := readVerifiedGPTArray(r, h.backup, arrayBytes)
	if err != nil {
		return nil, 0, err
	}
	if !bytes.Equal(primary, backup) {
		return nil, 0, semantic("primary and backup GPT arrays differ")
	}
	return primary, uint64(entrySize), nil
}

func validGPTEntryGeometry(count, entrySize uint32) bool {
	if count == 0 || entrySize < 128 {
		return false
	}
	return uint64(count)*uint64(entrySize) <= 64<<20
}

func readVerifiedGPTArray(r io.ReaderAt, header []byte, arrayBytes uint64) ([]byte, error) {
	array, err := readAt(r, binary.LittleEndian.Uint64(header[72:80])*512, arrayBytes)
	if err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(array) != binary.LittleEndian.Uint32(header[88:92]) {
		return nil, semantic("GPT partition array CRC mismatch")
	}
	return array, nil
}

func findSingleESP(primary, entries []byte, entrySize uint64) (uint64, uint64, error) {
	var found int
	var start, end uint64
	for off := uint64(0); off < uint64(len(entries)); off += entrySize {
		e := entries[off : off+entrySize]
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
	if found != 1 || !withinUsableLBAs(start, end, first, last) {
		return 0, 0, semantic("invalid single ESP bounds")
	}
	return start, end, nil
}

func withinUsableLBAs(start, end, first, last uint64) bool {
	return start >= first && end <= last && start <= end
}

func verifyGPTHeader(h []byte, current, alternate uint64) error {
	if string(h[:8]) != "EFI PART" {
		return semantic("invalid GPT header")
	}
	if binary.LittleEndian.Uint64(h[24:32]) != current || binary.LittleEndian.Uint64(h[32:40]) != alternate {
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

// fatGeometry is the subset of the BPB the verifier relies on.
type fatGeometry struct {
	bytesPerSector, sectorsPerCluster, reserved, fatSectors, totalSectors uint64
	fats                                                                  uint64
	backupSector, fsInfoSector                                            uint64
}

func parseFATGeometry(boot []byte) fatGeometry {
	return fatGeometry{
		bytesPerSector:    uint64(binary.LittleEndian.Uint16(boot[11:13])),
		sectorsPerCluster: uint64(boot[13]),
		reserved:          uint64(binary.LittleEndian.Uint16(boot[14:16])),
		fats:              uint64(boot[16]),
		totalSectors:      uint64(binary.LittleEndian.Uint32(boot[32:36])),
		fatSectors:        uint64(binary.LittleEndian.Uint32(boot[36:40])),
		fsInfoSector:      uint64(binary.LittleEndian.Uint16(boot[48:50])),
		backupSector:      uint64(binary.LittleEndian.Uint16(boot[50:52])),
	}
}

func (g fatGeometry) valid(volumeSize uint64) bool {
	if g.bytesPerSector != 512 || g.fats != 2 {
		return false
	}
	if g.sectorsPerCluster == 0 || !isPowerOfTwo(g.sectorsPerCluster) {
		return false
	}
	if g.reserved < 2 || g.fatSectors == 0 {
		return false
	}
	return g.totalSectors*512 <= volumeSize
}

func isPowerOfTwo(v uint64) bool { return v&(v-1) == 0 }

func (g fatGeometry) sectorOffset(base, sector uint64) uint64 { return base + sector*g.bytesPerSector }
func (g fatGeometry) fatBytes() uint64                        { return g.fatSectors * g.bytesPerSector }

func openFAT(r io.ReaderAt, base, size uint64) (*fatVolume, error) {
	boot, g, err := readFATBootSector(r, base, size)
	if err != nil {
		return nil, err
	}
	if err := verifyFATBackupSector(r, base, g, boot); err != nil {
		return nil, err
	}
	if err := verifyFSInfo(r, base, g); err != nil {
		return nil, err
	}
	fat, err := readFATMirrors(r, base, g)
	if err != nil {
		return nil, err
	}
	return &fatVolume{r: r, base: base, size: size, fatGeometry: g, fat: fat, owners: map[uint32]string{}, files: map[string]fatFile{}}, nil
}

func readFATBootSector(r io.ReaderAt, base, size uint64) ([]byte, fatGeometry, error) {
	boot, err := readAt(r, base, 512)
	if err != nil {
		return nil, fatGeometry{}, err
	}
	if !validFATBootSector(boot) {
		return nil, fatGeometry{}, semantic("invalid FAT32 boot sector")
	}
	g := parseFATGeometry(boot)
	if !g.valid(size) {
		return nil, fatGeometry{}, semantic("invalid FAT32 BPB")
	}
	return boot, g, nil
}

func validFATBootSector(boot []byte) bool {
	return hasBootSignature(boot) && string(boot[82:90]) == "FAT32   "
}

func verifyFATBackupSector(r io.ReaderAt, base uint64, g fatGeometry, boot []byte) error {
	backup, err := readAt(r, g.sectorOffset(base, g.backupSector), g.bytesPerSector)
	if err != nil || !bytes.Equal(boot, backup) {
		return semantic("FAT32 backup boot sector mismatch")
	}
	return nil
}

func verifyFSInfo(r io.ReaderAt, base uint64, g fatGeometry) error {
	info, err := readAt(r, g.sectorOffset(base, g.fsInfoSector), g.bytesPerSector)
	if err != nil {
		return err
	}
	if !validFSInfo(info) {
		return semantic("invalid FAT32 FSInfo")
	}
	backupInfo, err := readAt(r, g.sectorOffset(base, g.backupSector+g.fsInfoSector), g.bytesPerSector)
	if err != nil || !bytes.Equal(info, backupInfo) {
		return semantic("FAT32 backup FSInfo mismatch")
	}
	return nil
}

func validFSInfo(info []byte) bool {
	const lead, structural, trail = 0x41615252, 0x61417272, 0xaa550000
	if binary.LittleEndian.Uint32(info[:4]) != lead {
		return false
	}
	if binary.LittleEndian.Uint32(info[484:488]) != structural {
		return false
	}
	return binary.LittleEndian.Uint32(info[508:512]) == trail
}

func readFATMirrors(r io.ReaderAt, base uint64, g fatGeometry) ([]byte, error) {
	fat1, err := readAt(r, g.sectorOffset(base, g.reserved), g.fatBytes())
	if err != nil {
		return nil, err
	}
	fat2, err := readAt(r, g.sectorOffset(base, g.reserved+g.fatSectors), g.fatBytes())
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(fat1, fat2) {
		return nil, semantic("FAT mirrors differ")
	}
	return fat1, nil
}

func (v *fatVolume) clusterSize() uint64 { return v.bytesPerSector * v.sectorsPerCluster }
func (v *fatVolume) maxCluster() uint32 {
	return uint32((v.totalSectors-v.reserved-2*v.fatSectors)/v.sectorsPerCluster + 1)
}

const (
	fatEndOfChain = 0x0ffffff8
	fatEntryMask  = 0x0fffffff
)

func (v *fatVolume) chain(first uint32, owner string) ([]uint32, error) {
	if first < 2 {
		return nil, nil
	}
	seen := map[uint32]bool{}
	var out []uint32
	for c := first; c < fatEndOfChain; c = v.next(c) {
		if err := v.claimCluster(c, owner, seen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (v *fatVolume) validCluster(c uint32) bool {
	if c < 2 || c > v.maxCluster() {
		return false
	}
	return int(c)*4+4 <= len(v.fat)
}

func (v *fatVolume) claimCluster(c uint32, owner string, seen map[uint32]bool) error {
	if !v.validCluster(c) {
		return semantic("cluster out of bounds for %s", owner)
	}
	if seen[c] {
		return semantic("cluster loop for %s", owner)
	}
	if old, ok := v.owners[c]; ok && old != owner {
		return semantic("cluster cross-link between %s and %s", old, owner)
	}
	seen[c] = true
	v.owners[c] = owner
	return nil
}

func (v *fatVolume) next(c uint32) uint32 {
	return binary.LittleEndian.Uint32(v.fat[int(c)*4:int(c)*4+4]) & fatEntryMask
}

func (v *fatVolume) cluster(c uint32) ([]byte, error) {
	off := v.base + (v.reserved+2*v.fatSectors+uint64(c-2)*v.sectorsPerCluster)*v.bytesPerSector
	return readAt(v.r, off, v.clusterSize())
}

func (v *fatVolume) readChain(ctx context.Context, chain []uint32) ([]byte, error) {
	var data []byte
	for _, c := range chain {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := v.cluster(c)
		if err != nil {
			return nil, err
		}
		data = append(data, b...)
	}
	return data, nil
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
	data, err := v.readChain(ctx, chain)
	if err != nil {
		return err
	}
	for _, e := range parseDirEntries(data) {
		if err := v.addEntry(ctx, e, parent, dirs); err != nil {
			return err
		}
	}
	return nil
}

// dirEntry is a directory record with its long name already resolved.
type dirEntry struct {
	name    string
	attr    byte
	cluster uint32
	size    uint64
}

func (e dirEntry) isDir() bool { return e.attr&0x10 != 0 }

// parseDirEntries resolves long-name runs and drops deleted entries, dot
// entries, and volume labels, leaving only real files and subdirectories.
func parseDirEntries(data []byte) []dirEntry {
	var out []dirEntry
	var lfn [][]byte
	for off := 0; off+32 <= len(data); off += 32 {
		raw := data[off : off+32]
		if raw[0] == 0 {
			break
		}
		if raw[0] == 0xe5 {
			lfn = nil
			continue
		}
		if raw[11] == 0x0f {
			lfn = append(lfn, append([]byte(nil), raw...))
			continue
		}
		e := decodeDirEntry(raw, lfn)
		lfn = nil
		if !skipDirEntry(e) {
			out = append(out, e)
		}
	}
	return out
}

func decodeDirEntry(raw []byte, lfn [][]byte) dirEntry {
	name := shortName(raw)
	if len(lfn) > 0 {
		name = longName(lfn)
	}
	return dirEntry{
		name:    name,
		attr:    raw[11],
		cluster: uint32(binary.LittleEndian.Uint16(raw[26:28])) | uint32(binary.LittleEndian.Uint16(raw[20:22]))<<16,
		size:    uint64(binary.LittleEndian.Uint32(raw[28:32])),
	}
}

func skipDirEntry(e dirEntry) bool {
	if e.attr&0x08 != 0 {
		return true
	}
	return e.name == "" || e.name == "." || e.name == ".."
}

func (v *fatVolume) addEntry(ctx context.Context, e dirEntry, parent string, dirs map[uint32]bool) error {
	p := strings.ToLower(e.name)
	if parent != "" {
		p = parent + "/" + p
	}
	if e.isDir() {
		return v.walkDir(ctx, e.cluster, p, dirs)
	}
	if _, exists := v.files[p]; exists {
		return semantic("duplicate FAT path %s", p)
	}
	v.files[p] = fatFile{p, e.size, e.cluster}
	_, err := v.chain(e.cluster, "file:"+p)
	return err
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
	parts, err := collectSWMParts(files)
	if err != nil {
		return 0, err
	}
	if o.RequireSplitWIM && len(parts) == 0 {
		return 0, semantic("split WIM set missing")
	}
	return len(parts), verifySWMSet(parts, o.SplitWIMPolicySize)
}

func collectSWMParts(files map[string]fatFile) (map[int]fatFile, error) {
	parts := map[int]fatFile{}
	for p, f := range files {
		n, ok, err := swmPartNumber(p)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if _, dup := parts[n]; dup {
			return nil, semantic("duplicate SWM part")
		}
		parts[n] = f
	}
	return parts, nil
}

// swmPartNumber reports the 1-based index of a split WIM part path. Paths
// that look like parts but do not round-trip through the canonical name are
// rejected rather than ignored.
func swmPartNumber(p string) (int, bool, error) {
	if p == "sources/install.swm" {
		return 1, true, nil
	}
	if !strings.HasPrefix(p, "sources/install") || !strings.HasSuffix(p, ".swm") {
		return 0, false, nil
	}
	var n int
	if _, err := fmt.Sscanf(p, "sources/install%d.swm", &n); err != nil {
		return 0, false, semantic("invalid SWM name %s", p)
	}
	if n < 2 || p != fmt.Sprintf("sources/install%d.swm", n) {
		return 0, false, semantic("invalid SWM name %s", p)
	}
	return n, true, nil
}

func verifySWMSet(parts map[int]fatFile, policySize uint64) error {
	for i := 1; i <= len(parts); i++ {
		f, ok := parts[i]
		if !ok {
			return semantic("non-contiguous SWM set")
		}
		if f.size == 0 || f.size > policySize {
			return semantic("invalid SWM part size")
		}
	}
	return nil
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
