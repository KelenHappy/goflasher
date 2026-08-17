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
	if !os.SameFile(i.source.identity, current) || i.source.identity.Size() != current.Size() || !i.source.identity.ModTime().Equal(current.ModTime()) {
		return fmt.Errorf("%w: inspected source file changed", ErrUnsupported)
	}
	return nil
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
	h := sha256.New()
	buf := make([]byte, 4<<20)
	var size uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			size += uint64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if size != i.UncompressedSize || hex.EncodeToString(h.Sum(nil)) != i.SHA256 {
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
		if ownFile {
			_ = f.Close()
		}
		return nil, err
	}
	var reader io.Reader = f
	var closers []io.Closer
	if ownFile {
		closers = append(closers, f)
	}
	switch info.Compression {
	case CompressionNone:
	case CompressionGzip:
		gz, err := gzip.NewReader(f)
		if err != nil {
			if ownFile {
				_ = f.Close()
			}
			return nil, err
		}
		reader, closers = gz, append([]io.Closer{gz}, closers...)
	case CompressionXZ:
		// Go's standard library does not include XZ. Use a pure-Go streaming
		// decoder so every packaged platform has identical support without an
		// external executable or an uncompressed temporary file.
		if err := requireXZStreamFooter(f); err != nil {
			if ownFile {
				_ = f.Close()
			}
			return nil, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			if ownFile {
				_ = f.Close()
			}
			return nil, err
		}
		xr, err := xz.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		reader = xr
	default:
		if ownFile {
			_ = f.Close()
		}
		return nil, ErrUnsupported
	}
	return &ReadCloser{Reader: bufio.NewReaderSize(reader, 1<<20), closers: closers}, nil
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
	created := false
	if info.source == nil {
		f, err := os.Open(info.Path)
		if err != nil {
			return Info{}, err
		}
		info.source = &Source{file: f}
		created = true
	}
	r, err := openFile(info, info.source.file, false)
	if err != nil {
		if created {
			_ = info.CloseSource()
		}
		return Info{}, err
	}
	defer r.Close()
	h := sha256.New()
	buf := make([]byte, 4<<20)
	var n uint64
	for {
		if err := ctx.Err(); err != nil {
			if created {
				_ = info.CloseSource()
			}
			return Info{}, err
		}
		read, readErr := r.Read(buf)
		if read > 0 {
			_, _ = h.Write(buf[:read])
			n += uint64(read)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if created {
				_ = info.CloseSource()
			}
			return Info{}, readErr
		}
	}
	info.UncompressedSize = n
	info.SHA256 = hex.EncodeToString(h.Sum(nil))
	identity, err := info.source.file.Stat()
	if err != nil {
		if created {
			_ = info.CloseSource()
		}
		return Info{}, err
	}
	info.source.identity = identity
	return info, nil
}
