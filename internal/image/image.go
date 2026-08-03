package image

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

type commandCloser struct {
	pipe io.Closer
	cmd  *exec.Cmd
}

func (c *commandCloser) Close() error {
	pipeErr := c.pipe.Close()
	waitErr := c.cmd.Wait()
	// Closing a partially consumed pipe intentionally gives xz SIGPIPE.
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.Is(waitErr, os.ErrProcessDone) && !errors.As(waitErr, &exitErr) {
		return errors.Join(pipeErr, waitErr)
	}
	return pipeErr
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
	f, err := os.Open(info.Path)
	if err != nil {
		return nil, err
	}
	var reader io.Reader = f
	closers := []io.Closer{f}
	switch info.Compression {
	case CompressionNone:
	case CompressionGzip:
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		reader, closers = gz, []io.Closer{gz, f}
	case CompressionXZ:
		// Go's standard library does not include XZ. Invoke the ubiquitous
		// xz decoder without a shell; stdout remains a bounded streaming pipe.
		// The input path is supplied after -- to prevent option injection.
		_ = f.Close()
		cmd := exec.Command("xz", "--decompress", "--stdout", "--", info.Path)
		xr, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			_ = xr.Close()
			return nil, err
		}
		reader, closers = xr, []io.Closer{&commandCloser{pipe: xr, cmd: cmd}}
	default:
		f.Close()
		return nil, ErrUnsupported
	}
	return &ReadCloser{Reader: bufio.NewReaderSize(reader, 1<<20), closers: closers}, nil
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
	r, err := Open(info)
	if err != nil {
		return Info{}, err
	}
	defer r.Close()
	h := sha256.New()
	buf := make([]byte, 4<<20)
	var n uint64
	for {
		if err := ctx.Err(); err != nil {
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
			return Info{}, readErr
		}
	}
	info.UncompressedSize = n
	info.SHA256 = hex.EncodeToString(h.Sum(nil))
	return info, nil
}
