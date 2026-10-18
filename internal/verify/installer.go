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
	// Progress, when set, receives the file bytes hashed so far and the total
	// bytes listed in the manifest.
	Progress func(verified, total uint64)
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

// lba is the index of a 512-byte logical block on the raw target.
type lba uint64

func (l lba) offset() uint64 { return uint64(l) * 512 }

// lbaRange is an inclusive span of logical blocks.
type lbaRange struct{ first, last lba }

// within reports whether r is a non-empty span inside outer.
func (r lbaRange) within(outer lbaRange) bool {
	return r.first >= outer.first && r.last <= outer.last && r.first <= r.last
}

func (r lbaRange) size() uint64 { return uint64(r.last-r.first+1) * 512 }

// volumePath is a FAT path in the form the verifier compares: lower case,
// slash separated, and relative to the root directory.
type volumePath string

func manifestPath(p string) volumePath {
	return volumePath(strings.ToLower(strings.ReplaceAll(p, "\\", "/")))
}

func (p volumePath) child(name string) volumePath {
	name = strings.ToLower(name)
	if p == "" {
		return volumePath(name)
	}
	return p + "/" + volumePath(name)
}

// chainOwner names the directory or file a cluster chain belongs to. The
// directory walk and hashing both claim a file's chain, so they must name the
// same owner or the cross-link check rejects it.
type chainOwner string

func (p volumePath) dirOwner() chainOwner  { return chainOwner("dir:" + p) }
func (p volumePath) fileOwner() chainOwner { return chainOwner("file:" + p) }

// cluster is a FAT32 cluster number; data clusters start at 2.
type cluster uint32

const (
	fatEndOfChain cluster = 0x0ffffff8
	fatEntryMask          = 0x0fffffff
)

// fatRegion is the byte range of the raw target that holds the FAT32 volume.
type fatRegion struct {
	r          io.ReaderAt
	base, size uint64
}

func newFATRegion(r io.ReaderAt, extent lbaRange) fatRegion {
	return fatRegion{r: r, base: extent.first.offset(), size: extent.size()}
}

func (p fatRegion) read(off, n uint64) ([]byte, error)  { return readAt(p.r, p.base+off, n) }
func (p fatRegion) readFull(off uint64, b []byte) error { return readFullAt(p.r, p.base+off, b) }

type fatVolume struct {
	fatRegion
	fatGeometry
	fat    []byte
	owners map[cluster]chainOwner
	files  map[volumePath]fatFile
}
type fatFile struct {
	path  volumePath
	size  uint64
	first cluster
}

var requiredInstallerFiles = []volumePath{"efi/boot/bootx64.efi", "sources/boot.wim"}

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
	filesVerified, err := v.verifyManifest(ctx, expected, newVerifyMeter(manifest, options.Progress))
	result.FilesVerified = filesVerified
	if err != nil {
		return result, err
	}
	if err = verifyInstallerLayout(v.files, options); err != nil {
		return result, err
	}
	wimParts, err := verifySWM(v.files, options)
	result.WIMParts = wimParts
	if err != nil {
		return result, err
	}
	result.ManifestSHA256 = manifestHash(manifest)
	return result, nil
}

func openInstallerVolume(ctx context.Context, target RawTarget) (*fatVolume, error) {
	esp, err := verifyGPT(target)
	if err != nil {
		return nil, errors.Join(ErrGPTLayout, err)
	}
	v, err := openFAT(newFATRegion(target.Reader, esp))
	if err != nil {
		return nil, errors.Join(ErrFAT32Filesystem, err)
	}
	if err = v.walk(ctx); err != nil {
		return nil, errors.Join(ErrFAT32Filesystem, err)
	}
	return v, nil
}

func indexManifest(manifest []installer.VerificationEntry) (map[volumePath]installer.VerificationEntry, error) {
	expected := make(map[volumePath]installer.VerificationEntry, len(manifest))
	for _, e := range manifest {
		key := manifestPath(e.Path)
		if _, ok := expected[key]; ok {
			return nil, semantic("duplicate manifest path %s", e.Path)
		}
		expected[key] = e
	}
	return expected, nil
}

// verifyMeter accumulates hashed file bytes for InstallerOptions.Progress.
type verifyMeter struct {
	report      func(verified, total uint64)
	done, total uint64
}

func newVerifyMeter(manifest []installer.VerificationEntry, report func(uint64, uint64)) *verifyMeter {
	m := &verifyMeter{report: report}
	for _, e := range manifest {
		m.total += e.Size
	}
	m.add(0) // start the stage's throughput clock before the first read
	return m
}

func (m *verifyMeter) add(n uint64) {
	m.done += n
	if m.report != nil {
		m.report(min(m.done, m.total), m.total)
	}
}

func (v *fatVolume) verifyManifest(ctx context.Context, expected map[volumePath]installer.VerificationEntry, meter *verifyMeter) (int, error) {
	verified := 0
	for key, e := range expected {
		if err := v.verifyEntry(ctx, key, e, meter); err != nil {
			return verified, err
		}
		verified++
	}
	return verified, nil
}

func (v *fatVolume) verifyEntry(ctx context.Context, key volumePath, e installer.VerificationEntry, meter *verifyMeter) error {
	actual, ok := v.files[key]
	if !ok {
		return semantic("manifest path missing: %s", e.Path)
	}
	if actual.size != e.Size {
		return semantic("size mismatch for %s", e.Path)
	}
	h, err := v.hashFile(ctx, actual, meter)
	if err != nil {
		return err
	}
	if h != e.SHA256 {
		return semantic("hash mismatch for %s", e.Path)
	}
	return nil
}

func verifyInstallerLayout(files map[volumePath]fatFile, options InstallerOptions) error {
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

// sector is one raw sector read from the target.
type sector []byte

func readSector(r io.ReaderAt, at lba) (sector, error) { return readAt(r, at.offset(), 512) }

func (s sector) hasBootSignature() bool { return s[510] == 0x55 && s[511] == 0xaa }
func (s sector) isProtectiveMBR() bool  { return s.hasBootSignature() && s[450] == 0xee }
func (s sector) isFAT32Boot() bool {
	return s.hasBootSignature() && string(s[82:90]) == "FAT32   "
}

func (s sector) isFSInfo() bool {
	const lead, structural, trail = 0x41615252, 0x61417272, 0xaa550000
	if binary.LittleEndian.Uint32(s[:4]) != lead {
		return false
	}
	if binary.LittleEndian.Uint32(s[484:488]) != structural {
		return false
	}
	return binary.LittleEndian.Uint32(s[508:512]) == trail
}

// gptHeaders holds the primary and backup header sectors, which verifyGPT
// validates independently before comparing them with each other.
type gptHeaders struct {
	primary, backup gptHeader
	totalSectors    lba
}

func verifyGPT(target RawTarget) (lbaRange, error) {
	if err := verifyProtectiveMBR(target.Reader); err != nil {
		return lbaRange{}, err
	}
	h, err := readGPTHeaders(target)
	if err != nil {
		return lbaRange{}, err
	}
	array, err := readGPTArray(target.Reader, h)
	if err != nil {
		return lbaRange{}, err
	}
	return array.singleESP(h.primary.usable())
}

func verifyProtectiveMBR(r io.ReaderAt) error {
	mbr, err := readSector(r, 0)
	if err != nil {
		return err
	}
	if !mbr.isProtectiveMBR() {
		return semantic("invalid protective MBR")
	}
	return nil
}

func readGPTHeaders(target RawTarget) (gptHeaders, error) {
	total := lba(target.Size / 512)
	if total < 68 {
		return gptHeaders{}, semantic("target too small for GPT")
	}
	primary, err := readGPTHeader(target.Reader, 1, total-1)
	if err != nil {
		return gptHeaders{}, err
	}
	backup, err := readGPTHeader(target.Reader, total-1, 1)
	if err != nil {
		return gptHeaders{}, err
	}
	if !primary.sameMetadata(backup) {
		return gptHeaders{}, semantic("primary and backup GPT metadata differ")
	}
	return gptHeaders{primary: primary, backup: backup, totalSectors: total}, nil
}

// readGPTHeader reads the header at the given LBA and checks that it names
// itself and its alternate correctly.
func readGPTHeader(r io.ReaderAt, at, alternate lba) (gptHeader, error) {
	s, err := readSector(r, at)
	if err != nil {
		return nil, err
	}
	h := gptHeader(s)
	if err = h.verify(at, alternate); err != nil {
		return nil, err
	}
	return h, nil
}

// gptHeader is a raw GPT header sector.
type gptHeader []byte

func (h gptHeader) usable() lbaRange {
	return lbaRange{lba(binary.LittleEndian.Uint64(h[40:48])), lba(binary.LittleEndian.Uint64(h[48:56]))}
}
func (h gptHeader) arrayStart() lba  { return lba(binary.LittleEndian.Uint64(h[72:80])) }
func (h gptHeader) arrayCRC() uint32 { return binary.LittleEndian.Uint32(h[88:92]) }
func (h gptHeader) entryGeometry() gptEntryGeometry {
	return gptEntryGeometry{count: binary.LittleEndian.Uint32(h[80:84]), size: binary.LittleEndian.Uint32(h[84:88])}
}

// sameMetadata compares the disk GUID and the usable LBA range, which must
// agree between the primary and backup headers.
func (h gptHeader) sameMetadata(other gptHeader) bool {
	if !bytes.Equal(h[56:72], other[56:72]) {
		return false
	}
	return bytes.Equal(h[40:56], other[40:56])
}

func (h gptHeader) verify(current, alternate lba) error {
	if string(h[:8]) != "EFI PART" {
		return semantic("invalid GPT header")
	}
	if lba(binary.LittleEndian.Uint64(h[24:32])) != current || lba(binary.LittleEndian.Uint64(h[32:40])) != alternate {
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

// readArray reads the partition entry array the header points to and checks
// it against the header's CRC.
func (h gptHeader) readArray(r io.ReaderAt, g gptEntryGeometry) ([]byte, error) {
	array, err := readAt(r, h.arrayStart().offset(), g.bytes())
	if err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(array) != h.arrayCRC() {
		return nil, semantic("GPT partition array CRC mismatch")
	}
	return array, nil
}

// gptEntryGeometry is the partition entry count and size a GPT header declares.
type gptEntryGeometry struct{ count, size uint32 }

func (g gptEntryGeometry) bytes() uint64 { return uint64(g.count) * uint64(g.size) }

func (g gptEntryGeometry) valid() bool {
	if g.count == 0 || g.size < 128 {
		return false
	}
	return g.bytes() <= 64<<20
}

func readGPTArray(r io.ReaderAt, h gptHeaders) (gptArray, error) {
	g := h.primary.entryGeometry()
	if !g.valid() {
		return gptArray{}, semantic("invalid GPT entry geometry")
	}
	primary, err := h.primary.readArray(r, g)
	if err != nil {
		return gptArray{}, err
	}
	backup, err := h.backup.readArray(r, g)
	if err != nil {
		return gptArray{}, err
	}
	if !bytes.Equal(primary, backup) {
		return gptArray{}, semantic("primary and backup GPT arrays differ")
	}
	return gptArray{entries: primary, entrySize: uint64(g.size)}, nil
}

// gptArray is a verified partition entry array.
type gptArray struct {
	entries   []byte
	entrySize uint64
}

// singleESP returns the extent of the array's only partition, which must be
// an ESP inside the usable LBAs.
func (a gptArray) singleESP(usable lbaRange) (lbaRange, error) {
	var found int
	var esp lbaRange
	for off := uint64(0); off < uint64(len(a.entries)); off += a.entrySize {
		e := gptEntry(a.entries[off : off+a.entrySize])
		if e.unused() {
			continue
		}
		found++
		if !e.isESP() {
			return lbaRange{}, semantic("non-ESP GPT partition")
		}
		esp = e.extent()
	}
	if found != 1 || !esp.within(usable) {
		return lbaRange{}, semantic("invalid single ESP bounds")
	}
	return esp, nil
}

// gptEntry is one raw partition entry.
type gptEntry []byte

func (e gptEntry) unused() bool { return bytes.Equal(e[:16], make([]byte, 16)) }
func (e gptEntry) isESP() bool  { return bytes.Equal(e[:16], gpt.EFITypeGUID.MarshalBinary()) }
func (e gptEntry) extent() lbaRange {
	return lbaRange{lba(binary.LittleEndian.Uint64(e[32:40])), lba(binary.LittleEndian.Uint64(e[40:48]))}
}

// fatGeometry is the subset of the BPB the verifier relies on.
type fatGeometry struct {
	bytesPerSector, sectorsPerCluster, reserved, fatSectors, totalSectors uint64
	fats                                                                  uint64
	backupSector, fsInfoSector                                            uint64
}

func parseFATGeometry(boot sector) fatGeometry {
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

// valid reports whether the geometry is one the verifier supports and fits
// inside the region it was read from.
func (g fatGeometry) valid(p fatRegion) bool {
	if g.bytesPerSector != 512 || g.fats != 2 {
		return false
	}
	if g.sectorsPerCluster == 0 || !isPowerOfTwo(g.sectorsPerCluster) {
		return false
	}
	if g.reserved < 2 || g.fatSectors == 0 {
		return false
	}
	return g.totalSectors*512 <= p.size
}

func isPowerOfTwo(v uint64) bool { return v&(v-1) == 0 }

func (g fatGeometry) sectorOffset(s uint64) uint64 { return s * g.bytesPerSector }
func (g fatGeometry) fatBytes() uint64             { return g.fatSectors * g.bytesPerSector }

func openFAT(p fatRegion) (*fatVolume, error) {
	boot, g, err := readFATBootSector(p)
	if err != nil {
		return nil, err
	}
	v := &fatVolume{fatRegion: p, fatGeometry: g, owners: map[cluster]chainOwner{}, files: map[volumePath]fatFile{}}
	if err = v.verifyBackupBoot(boot); err != nil {
		return nil, err
	}
	if err = v.verifyFSInfo(); err != nil {
		return nil, err
	}
	if v.fat, err = v.readFATMirrors(); err != nil {
		return nil, err
	}
	return v, nil
}

func readFATBootSector(p fatRegion) (sector, fatGeometry, error) {
	b, err := p.read(0, 512)
	if err != nil {
		return nil, fatGeometry{}, err
	}
	boot := sector(b)
	if !boot.isFAT32Boot() {
		return nil, fatGeometry{}, semantic("invalid FAT32 boot sector")
	}
	g := parseFATGeometry(boot)
	if !g.valid(p) {
		return nil, fatGeometry{}, semantic("invalid FAT32 BPB")
	}
	return boot, g, nil
}

func (v *fatVolume) readSector(s uint64) (sector, error) {
	return v.read(v.sectorOffset(s), v.bytesPerSector)
}

func (v *fatVolume) verifyBackupBoot(boot sector) error {
	backup, err := v.readSector(v.backupSector)
	if err != nil || !bytes.Equal(boot, backup) {
		return semantic("FAT32 backup boot sector mismatch")
	}
	return nil
}

func (v *fatVolume) verifyFSInfo() error {
	info, err := v.readSector(v.fsInfoSector)
	if err != nil {
		return err
	}
	if !info.isFSInfo() {
		return semantic("invalid FAT32 FSInfo")
	}
	backupInfo, err := v.readSector(v.backupSector + v.fsInfoSector)
	if err != nil || !bytes.Equal(info, backupInfo) {
		return semantic("FAT32 backup FSInfo mismatch")
	}
	return nil
}

func (v *fatVolume) readFATMirrors() ([]byte, error) {
	fat1, err := v.read(v.sectorOffset(v.reserved), v.fatBytes())
	if err != nil {
		return nil, err
	}
	fat2, err := v.read(v.sectorOffset(v.reserved+v.fatSectors), v.fatBytes())
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(fat1, fat2) {
		return nil, semantic("FAT mirrors differ")
	}
	return fat1, nil
}

func (v *fatVolume) clusterSize() uint64 { return v.bytesPerSector * v.sectorsPerCluster }
func (v *fatVolume) maxCluster() cluster {
	return cluster((v.totalSectors-v.reserved-2*v.fatSectors)/v.sectorsPerCluster + 1)
}

func (v *fatVolume) chain(first cluster, owner chainOwner) ([]cluster, error) {
	if first < 2 {
		return nil, nil
	}
	seen := map[cluster]bool{}
	var out []cluster
	for c := first; c < fatEndOfChain; c = v.next(c) {
		if err := v.claimCluster(c, owner, seen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (v *fatVolume) validCluster(c cluster) bool {
	if c < 2 || c > v.maxCluster() {
		return false
	}
	return int(c)*4+4 <= len(v.fat)
}

func (v *fatVolume) claimCluster(c cluster, owner chainOwner, seen map[cluster]bool) error {
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

func (v *fatVolume) next(c cluster) cluster {
	return cluster(binary.LittleEndian.Uint32(v.fat[int(c)*4:int(c)*4+4]) & fatEntryMask)
}

func (v *fatVolume) readCluster(c cluster) ([]byte, error) {
	return v.read(v.clusterOffset(c), v.clusterSize())
}

// clusterOffset is the byte offset of c from the start of the volume.
func (v *fatVolume) clusterOffset(c cluster) uint64 {
	return (v.reserved + 2*v.fatSectors + uint64(c-2)*v.sectorsPerCluster) * v.bytesPerSector
}

func (v *fatVolume) readChain(ctx context.Context, chain []cluster) ([]byte, error) {
	var data []byte
	for _, c := range chain {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := v.readCluster(c)
		if err != nil {
			return nil, err
		}
		data = append(data, b...)
	}
	return data, nil
}

func (v *fatVolume) walk(ctx context.Context) error { return v.walkDir(ctx, 2, "", map[cluster]bool{}) }

func (v *fatVolume) walkDir(ctx context.Context, first cluster, parent volumePath, dirs map[cluster]bool) error {
	if dirs[first] {
		return semantic("directory cycle")
	}
	dirs[first] = true
	defer delete(dirs, first)
	chain, err := v.chain(first, parent.dirOwner())
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
	name  string
	attr  byte
	first cluster
	size  uint64
}

func (e dirEntry) isDir() bool { return e.attr&0x10 != 0 }

// skipped reports volume labels and nameless or dot entries, which are not
// real files or subdirectories.
func (e dirEntry) skipped() bool {
	if e.attr&0x08 != 0 {
		return true
	}
	return e.name == "" || e.name == "." || e.name == ".."
}

// rawDirEntry is one 32-byte directory record as stored on disk.
type rawDirEntry []byte

func (r rawDirEntry) endOfDir() bool    { return r[0] == 0 }
func (r rawDirEntry) deleted() bool     { return r[0] == 0xe5 }
func (r rawDirEntry) isLFN() bool       { return r[11] == 0x0f }
func (r rawDirEntry) lfnSequence() byte { return r[0] & 0x1f }

// parseDirEntries resolves long-name runs and drops deleted entries, dot
// entries, and volume labels, leaving only real files and subdirectories.
func parseDirEntries(data []byte) []dirEntry {
	var out []dirEntry
	var lfn []rawDirEntry
	for off := 0; off+32 <= len(data); off += 32 {
		raw := rawDirEntry(data[off : off+32])
		if raw.endOfDir() {
			break
		}
		if raw.deleted() {
			lfn = nil
			continue
		}
		if raw.isLFN() {
			lfn = append(lfn, append(rawDirEntry(nil), raw...))
			continue
		}
		e := raw.decode(lfn)
		lfn = nil
		if !e.skipped() {
			out = append(out, e)
		}
	}
	return out
}

func (r rawDirEntry) decode(lfn []rawDirEntry) dirEntry {
	name := r.shortName()
	if len(lfn) > 0 {
		name = longName(lfn)
	}
	return dirEntry{
		name:  name,
		attr:  r[11],
		first: cluster(binary.LittleEndian.Uint16(r[26:28])) | cluster(binary.LittleEndian.Uint16(r[20:22]))<<16,
		size:  uint64(binary.LittleEndian.Uint32(r[28:32])),
	}
}

func (v *fatVolume) addEntry(ctx context.Context, e dirEntry, parent volumePath, dirs map[cluster]bool) error {
	p := parent.child(e.name)
	if e.isDir() {
		return v.walkDir(ctx, e.first, p, dirs)
	}
	if _, exists := v.files[p]; exists {
		return semantic("duplicate FAT path %s", p)
	}
	v.files[p] = fatFile{p, e.size, e.first}
	_, err := v.chain(e.first, p.fileOwner())
	return err
}

func (r rawDirEntry) shortName() string {
	base := strings.TrimSpace(string(r[:8]))
	ext := strings.TrimSpace(string(r[8:11]))
	if ext != "" {
		return base + "." + ext
	}
	return base
}

// lfnField is a byte range holding UTF-16 name characters in a long-name
// directory entry.
type lfnField struct{ start, end int }

var lfnNameFields = []lfnField{{1, 11}, {14, 26}, {28, 32}}

func longName(es []rawDirEntry) string {
	sort.Slice(es, func(i, j int) bool { return es[i].lfnSequence() < es[j].lfnSequence() })
	var u []uint16
	for _, e := range es {
		for _, field := range lfnNameFields {
			u = e.appendLFNField(u, field)
		}
	}
	return string(utf16.Decode(u))
}

// appendLFNField appends the UTF-16 characters stored in one name field,
// stopping at the NUL terminator or 0xffff padding.
func (r rawDirEntry) appendLFNField(u []uint16, f lfnField) []uint16 {
	for i := f.start; i < f.end; i += 2 {
		c := binary.LittleEndian.Uint16(r[i : i+2])
		if c == 0 || c == 0xffff {
			return u
		}
		u = append(u, c)
	}
	return u
}

func (v *fatVolume) hashFile(ctx context.Context, f fatFile, meter *verifyMeter) (string, error) {
	chain, err := v.chain(f.first, f.path.fileOwner())
	if err != nil {
		return "", err
	}
	needed := (f.size + v.clusterSize() - 1) / v.clusterSize()
	if uint64(len(chain)) != needed {
		return "", semantic("invalid chain length for %s", f.path)
	}
	h := sha256.New()
	cs := v.clusterSize()
	limit := max(1, maxHashRead/cs)
	buf := make([]byte, limit*cs)
	remaining := f.size
	for len(chain) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		run := contiguousRun(chain, limit)
		b := buf[:run*cs]
		if err := v.readFull(v.clusterOffset(chain[0]), b); err != nil {
			return "", err
		}
		n := min(uint64(len(b)), remaining)
		h.Write(b[:n])
		remaining -= n
		meter.add(n)
		chain = chain[run:]
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// maxHashRead caps one device read while hashing file data.
const maxHashRead = uint64(1 << 20)

// contiguousRun counts the leading clusters of chain that directly follow one
// another on disk, at least one and at most limit, so they can be read at once.
func contiguousRun(chain []cluster, limit uint64) uint64 {
	n := uint64(1)
	for n < uint64(len(chain)) && n < limit && chain[n] == chain[n-1]+1 {
		n++
	}
	return n
}

func verifySWM(files map[volumePath]fatFile, o InstallerOptions) (int, error) {
	parts, err := collectSWMParts(files)
	if err != nil {
		return 0, err
	}
	if o.RequireSplitWIM && len(parts) == 0 {
		return 0, semantic("split WIM set missing")
	}
	return len(parts), parts.verify(o.SplitWIMPolicySize)
}

// swmParts maps each split WIM part's 1-based index to its file.
type swmParts map[int]fatFile

func collectSWMParts(files map[volumePath]fatFile) (swmParts, error) {
	parts := swmParts{}
	for p, f := range files {
		n, ok, err := p.swmPartNumber()
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
func (p volumePath) swmPartNumber() (int, bool, error) {
	if p == "sources/install.swm" {
		return 1, true, nil
	}
	if !strings.HasPrefix(string(p), "sources/install") || !strings.HasSuffix(string(p), ".swm") {
		return 0, false, nil
	}
	var n int
	if _, err := fmt.Sscanf(string(p), "sources/install%d.swm", &n); err != nil {
		return 0, false, semantic("invalid SWM name %s", p)
	}
	if n < 2 || string(p) != fmt.Sprintf("sources/install%d.swm", n) {
		return 0, false, semantic("invalid SWM name %s", p)
	}
	return n, true, nil
}

// verify checks that the parts are numbered 1..n without gaps and that each
// is non-empty and within the split policy size.
func (parts swmParts) verify(policySize uint64) error {
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
	if n > 64<<20 {
		return nil, semantic("read out of bounds")
	}
	b := make([]byte, n)
	if err := readFullAt(r, off, b); err != nil {
		return nil, err
	}
	return b, nil
}

func readFullAt(r io.ReaderAt, off uint64, b []byte) error {
	if off > uint64(^uint64(0)>>1) {
		return semantic("read out of bounds")
	}
	_, err := r.ReadAt(b, int64(off))
	return err
}

func semantic(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInstallerSemantic, fmt.Sprintf(format, args...))
}
