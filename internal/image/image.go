package image

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

type Format string
type Compression string

const (
	FormatISO       Format      = "iso"
	FormatIMG       Format      = "img"
	FormatRAW       Format      = "raw"
	CompressionNone Compression = "none"
	CompressionGzip Compression = "gzip"
	CompressionXZ   Compression = "xz"
)

var ErrUnsupported = errors.New("unsupported image")

type Info struct {
	Path             string
	Format           Format
	Compression      Compression
	CompressedSize   uint64
	UncompressedSize uint64 // zero when the stream does not advertise its size
	SHA256           string
	source           *Source
}

// Source owns the single pathname-opened file used for inspection and writing.
// Rewinding this descriptor recreates streaming decompressors without resolving
// the caller-controlled pathname a second time.
type Source struct {
	file     *os.File
	identity os.FileInfo
}

// RetainedReaderAt exposes the already-open, identity-checked source for
// parsers which require random access. The returned handle is borrowed: the
// Info remains responsible for closing it.
func (i Info) RetainedReaderAt() (io.ReaderAt, int64, io.Closer, error) {
	if err := i.ValidateSource(); err != nil {
		return nil, 0, nil, err
	}
	return i.source.file, i.source.identity.Size(), borrowedSource{i.source.file}, nil
}

type borrowedSource struct{ io.ReaderAt }

func (borrowedSource) Close() error { return nil }

func (i Info) HasRetainedSource() bool { return i.source != nil }

func (i Info) CloseSource() error {
	if i.source == nil {
		return nil
	}
	return i.source.file.Close()
}

// ValidateSource detects truncation and in-place metadata changes before a
// target is opened. Path replacement is irrelevant because source.file remains
// bound to the inode/Windows file object originally inspected.
func (i Info) ValidateSource() error {
	if i.source == nil || i.source.identity == nil {
		return fmt.Errorf("%w: inspected source handle is unavailable", ErrUnsupported)
	}
	current, err := i.source.file.Stat()
	if err != nil {
		return fmt.Errorf("validate inspected source: %w", err)
	}
	if !sameSourceMetadata(i.source.identity, current) {
		return fmt.Errorf("%w: inspected source file changed", ErrUnsupported)
	}
	return nil
}

func sameSourceMetadata(expected, current os.FileInfo) bool {
	if !os.SameFile(expected, current) {
		return false
	}
	if expected.Size() != current.Size() {
		return false
	}
	return expected.ModTime().Equal(current.ModTime())
}

// VerifySourceContext rereads the retained, decoded stream immediately before
// target preparation. This detects same-inode content changes even when an
// attacker preserves size and timestamps. The existing checksum calculated
// while writing remains a final defense against later concurrent modification.
func (i Info) VerifySourceContext(ctx context.Context) error {
	if err := i.ValidateSource(); err != nil {
		return err
	}
	r, err := Open(i)
	if err != nil {
		return err
	}
	defer r.Close()
	size, checksum, err := decodedDigest(ctx, r)
	if err != nil {
		return err
	}
	if size != i.UncompressedSize || checksum != i.SHA256 {
		return fmt.Errorf("%w: retained source bytes changed after inspection", ErrUnsupported)
	}
	return nil
}

// Detect validates both the filename and compression magic. Image payloads do
// not have one universal magic, so their supported extension remains decisive.
func Detect(path string) (Info, error) {
	file, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return Info{}, err
	}
	format, compression, err := formatFromName(path)
	if err != nil {
		return Info{}, err
	}
	magic := make([]byte, 6)
	n, readErr := io.ReadFull(file, magic)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return Info{}, readErr
	}
	if !magicMatchesCompression(magic[:n], compression) {
		return Info{}, fmt.Errorf("%w: extension and magic bytes disagree", ErrUnsupported)
	}
	return Info{Path: path, Format: format, Compression: compression, CompressedSize: uint64(stat.Size())}, nil
}

func formatFromName(path string) (Format, Compression, error) {
	base := strings.ToLower(filepath.Base(path))
	compression := CompressionNone
	for suffix, candidate := range map[string]Compression{".gz": CompressionGzip, ".xz": CompressionXZ} {
		if strings.HasSuffix(base, suffix) {
			compression, base = candidate, strings.TrimSuffix(base, suffix)
			break
		}
	}
	formats := map[string]Format{".iso": FormatISO, ".img": FormatIMG, ".raw": FormatRAW}
	extension := filepath.Ext(base)
	format, ok := formats[extension]
	if !ok {
		return "", "", fmt.Errorf("%w: extension %q", ErrUnsupported, extension)
	}
	return format, compression, nil
}

func magicMatchesCompression(magic []byte, compression Compression) bool {
	isGzip := len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b
	isXZ := len(magic) >= 6 && string(magic[:6]) == "\xfd7zXZ\x00"
	switch compression {
	case CompressionGzip:
		return isGzip
	case CompressionXZ:
		return isXZ
	default:
		return !isGzip && !isXZ
	}
}

type ReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (r *ReadCloser) Close() error {
	var result error
	for _, c := range r.closers {
		result = errors.Join(result, c.Close())
	}
	return result
}

// Open returns a buffered, streaming decompressor; it never materializes the
// uncompressed image on disk or in memory.
func Open(info Info) (*ReadCloser, error) {
	if info.source != nil {
		if err := info.ValidateSource(); err != nil {
			return nil, err
		}
		return openFile(info, info.source.file, false)
	}
	f, err := os.Open(info.Path)
	if err != nil {
		return nil, err
	}
	return openFile(info, f, true)
}

func openFile(info Info, f *os.File, ownFile bool) (*ReadCloser, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, closeOwnedFile(f, ownFile, err)
	}
	var closers []io.Closer
	if ownFile {
		closers = append(closers, f)
	}
	reader, decoderCloser, err := decodedReader(info.Compression, f)
	if err != nil {
		return nil, closeOwnedFile(f, ownFile, err)
	}
	if decoderCloser != nil {
		closers = append([]io.Closer{decoderCloser}, closers...)
	}
	return &ReadCloser{Reader: bufio.NewReaderSize(reader, 1<<20), closers: closers}, nil
}

func decodedReader(compression Compression, f *os.File) (io.Reader, io.Closer, error) {
	switch compression {
	case CompressionNone:
		return f, nil, nil
	case CompressionGzip:
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, nil, err
		}
		return gz, gz, nil
	case CompressionXZ:
		reader, err := openXZReader(f)
		return reader, nil, err
	default:
		return nil, nil, ErrUnsupported
	}
}

func openXZReader(f *os.File) (io.Reader, error) {
	// Go's standard library does not include XZ. Use a pure-Go streaming
	// decoder so every packaged platform has identical support.
	if err := requireXZStreamFooter(f); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return xz.NewReader(f)
}

func closeOwnedFile(f *os.File, owned bool, err error) error {
	if owned {
		return errors.Join(err, f.Close())
	}
	return err
}

// requireXZStreamFooter rejects XZ files that do not end with the two-byte
// stream footer magic. The pure-Go decoder treats a stream truncated right
// after its header as a valid empty stream, which would let an incomplete
// image be written silently; every complete XZ stream ends with "YZ", so a
// missing footer proves the file was cut short. f's read position is moved
// and must be rewound by the caller.
func requireXZStreamFooter(f *os.File) error {
	buf := make([]byte, 2)
	if _, err := f.Seek(-int64(len(buf)), io.SeekEnd); err != nil {
		return fmt.Errorf("%w: xz stream footer missing", ErrUnsupported)
	}
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("%w: xz stream footer missing", ErrUnsupported)
	}
	if !bytes.Equal(buf, []byte("YZ")) {
		return fmt.Errorf("%w: xz stream footer missing", ErrUnsupported)
	}
	return nil
}

func Checksum(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Inspect streams the decoded image once to establish the exact write length
// and source checksum before any target is unmounted or opened.
func Inspect(info Info) (Info, error) { return InspectContext(context.Background(), info) }

func InspectContext(ctx context.Context, info Info) (Info, error) {
	info, created, err := retainSource(info)
	if err != nil {
		return Info{}, err
	}
	r, err := openFile(info, info.source.file, false)
	if err != nil {
		return Info{}, closeCreatedSource(info, created, err)
	}
	defer r.Close()
	info.UncompressedSize, info.SHA256, err = decodedDigest(ctx, r)
	if err != nil {
		return Info{}, closeCreatedSource(info, created, err)
	}
	identity, err := info.source.file.Stat()
	if err != nil {
		return Info{}, closeCreatedSource(info, created, err)
	}
	info.source.identity = identity
	return info, nil
}

func retainSource(info Info) (Info, bool, error) {
	if info.source != nil {
		return info, false, nil
	}
	f, err := os.Open(info.Path)
	if err != nil {
		return Info{}, false, err
	}
	info.source = &Source{file: f}
	return info, true, nil
}

func closeCreatedSource(info Info, created bool, err error) error {
	if created {
		return errors.Join(err, info.CloseSource())
	}
	return err
}

type byteCounter uint64

func (c *byteCounter) Write(p []byte) (int, error) {
	*c += byteCounter(len(p))
	return len(p), nil
}

func decodedDigest(ctx context.Context, reader io.Reader) (uint64, string, error) {
	h := sha256.New()
	var size byteCounter
	source := contextReader{ctx: ctx, reader: reader}
	if _, err := io.CopyBuffer(io.MultiWriter(h, &size), source, make([]byte, 4<<20)); err != nil {
		return 0, "", err
	}
	return uint64(size), hex.EncodeToString(h.Sum(nil)), nil
}
