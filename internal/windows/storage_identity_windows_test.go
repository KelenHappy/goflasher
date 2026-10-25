//go:build windows

package windows

import (
	"encoding/binary"
	"testing"
)

func TestWindowsIdentityEvidence(t *testing.T) {
	tests := []struct {
		name, serial, wwn, id string
		valid                 bool
	}{
		{"serial only", " abc ", "", "windows:serial=ABC", true},
		{"WWN only", "", "5000c50012345678", "windows:wwn=5000C50012345678", true},
		{"serial and WWN", "abc", "5000c50012345678", "windows:serial=ABC;wwn=5000C50012345678", true},
		{"neither", "", "", "", true},
		{"duplicate evidence", "same", "SAME", "", false},
		{"control character", "bad\nserial", "", "", false},
		{"interior space", "ABC 123", "", "windows:serial=ABC 123", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := newWindowsIdentity(tt.serial, tt.wwn)
			if !tt.valid {
				requireError(t, err)
				return
			}
			requireNoError(t, err)
			requireEqual(t, "canonical ID", e.canonicalID(), tt.id)
		})
	}
}

func TestIdentityAdmissionRequiresSerialOrWWN(t *testing.T) {
	const wwn = "5000C50012345678"
	accepted := []windowsIdentityEvidence{
		{Serial: "SERIAL"},
		{WWN: wwn},
		{Serial: "SERIAL", WWN: wwn},
	}
	for _, evidence := range accepted {
		r := candidate()
		r.identity = evidence
		r.ID, r.Serial, r.WWN = evidence.canonicalID(), evidence.Serial, evidence.WWN
		classify(&r)
		if !r.IsAllowed {
			t.Fatalf("evidence %+v rejected: %s", evidence, r.RejectReason)
		}
	}
	r := candidate()
	r.identity, r.ID, r.Serial, r.WWN = windowsIdentityEvidence{}, "", "", ""
	classify(&r)
	if r.IsAllowed || r.RejectReason != "no trustworthy persistent identity" {
		t.Fatalf("allowed=%v reason=%q", r.IsAllowed, r.RejectReason)
	}
}

// TestParseStorageDeviceWWN ensures parseStorageDeviceIDs reads the WWN and
// rejects an incomplete identifier list. The descriptor is laid out by hand
// with the ntddstor.h offsets so the parser and the storageIdentifier helper
// cannot drift together.
func TestParseStorageDeviceWWN(t *testing.T) {
	b := make([]byte, 12+16+8)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[8:12], 1)
	binary.LittleEndian.PutUint32(b[12:16], 1) // CodeSet: binary
	binary.LittleEndian.PutUint32(b[16:20], 3) // Type: FCPH name (NAA)
	binary.LittleEndian.PutUint16(b[20:22], 8) // IdentifierSize
	binary.LittleEndian.PutUint16(b[22:24], 0) // NextOffset
	binary.LittleEndian.PutUint32(b[24:28], 0) // Association: device
	copy(b[28:], []byte{0x50, 0x00, 0xc5, 0x00, 0x12, 0x34, 0x56, 0x78})
	got, err := parseStorageDeviceIDs(b)
	if err != nil || got != "5000C50012345678" {
		t.Fatalf("WWN=%q error=%v", got, err)
	}
	binary.LittleEndian.PutUint32(b[8:12], 2)
	if _, err := parseStorageDeviceIDs(b); err == nil {
		t.Fatal("incomplete identifier list accepted")
	}
}

func TestParseStorageDeviceIDs(t *testing.T) {
	const wwn = "5000C50012345678"
	binaryWWN := storageIdentifier(1, 3, 0, []byte{0x50, 0x00, 0xc5, 0x00, 0x12, 0x34, 0x56, 0x78})
	otherWWN := storageIdentifier(1, 3, 0, []byte{0x60, 0, 0, 0, 0, 0, 0, 1})
	asciiWWN := storageIdentifier(2, 2, 0, []byte(" 5000c50012345678 \x00"))
	utf8WWN := storageIdentifier(3, 2, 0, []byte("5000c50012345678"))
	vendor := storageIdentifier(1, 0, 0, []byte{0xde, 0xad})
	ignored := storageIdentifier(1, 3, 1, []byte{0xde, 0xad})

	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{name: "binary NAA", data: deviceIDDescriptor(binaryWWN), want: wwn},
		{name: "normalized ASCII EUI", data: deviceIDDescriptor(asciiWWN), want: wwn},
		{name: "UTF-8 EUI", data: deviceIDDescriptor(utf8WWN), want: wwn},
		{name: "ignores vendor-specific type", data: deviceIDDescriptor(vendor, binaryWWN), want: wwn},
		{name: "no stable identifier", data: deviceIDDescriptor(vendor, ignored), want: ""},
		{name: "ignores non-device association", data: deviceIDDescriptor(ignored, binaryWWN), want: wwn},
		{name: "rejects conflicting identifiers", data: deviceIDDescriptor(binaryWWN, otherWWN), wantErr: true},
		{name: "rejects short descriptor", data: make([]byte, 11), wantErr: true},
		{name: "rejects undersized descriptor size", data: descriptorWithSize(11, 0, nil), wantErr: true},
		{name: "rejects oversized descriptor size", data: descriptorWithSize(64, 0, nil), wantErr: true},
		{name: "rejects missing declared entry", data: descriptorWithSize(12, 1, nil), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStorageDeviceIDs(tt.data)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("WWN=%q error=%v, want %q error=%t", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParseStorageIdentifierRejectsInvalidEntryBounds(t *testing.T) {
	const hdr = storageIdentifierHeaderSize
	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated header", data: make([]byte, hdr-1)},
		{name: "empty identifier", data: storageIdentifier(1, 3, 0, nil)},
		{name: "truncated identifier", data: storageIdentifierWithField([]byte{1}, 8, 2)},
		{name: "next overlaps identifier", data: storageIdentifierWithField([]byte{1, 2}, 10, hdr+1)},
		{name: "next exceeds buffer", data: storageIdentifierWithField([]byte{1}, 10, hdr+2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := (storageIdentifierEntry{descriptor: tt.data, index: 4}).parse(); err == nil {
				t.Fatal("invalid STORAGE_IDENTIFIER accepted")
			}
		})
	}
}

func TestStorageIdentifierListValidateLink(t *testing.T) {
	const hdr = storageIdentifierHeaderSize
	tests := []struct {
		name          string
		next          int
		parsed, count uint32
		wantError     bool
	}{
		{name: "middle entry links to next", next: hdr, parsed: 1, count: 2},
		{name: "middle entry ends list", parsed: 1, count: 2, wantError: true},
		{name: "last entry ends list", parsed: 2, count: 2},
		{name: "last entry links beyond count", next: hdr, parsed: 2, count: 2, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := storageIdentifierList{count: tt.count}
			err := list.validateLink(storageIdentifierEntry{index: tt.parsed - 1}, storageIdentifierHeader{next: tt.next})
			if (err != nil) != tt.wantError {
				t.Fatalf("validateLink(next %d, parsed %d, count %d) error = %v, want error %t",
					tt.next, tt.parsed, tt.count, err, tt.wantError)
			}
		})
	}
}

// storageIdentifier builds one STORAGE_IDENTIFIER with the ntddstor.h layout:
// 4-byte CodeSet and Type enums, USHORT IdentifierSize and NextOffset, 4-byte
// Association enum, then the identifier bytes.
func storageIdentifier(codeSet, identifierType, association uint32, value []byte) []byte {
	entry := make([]byte, storageIdentifierHeaderSize+len(value))
	binary.LittleEndian.PutUint32(entry[0:4], codeSet)
	binary.LittleEndian.PutUint32(entry[4:8], identifierType)
	binary.LittleEndian.PutUint16(entry[8:10], uint16(len(value)))
	binary.LittleEndian.PutUint32(entry[12:16], association)
	copy(entry[storageIdentifierHeaderSize:], value)
	return entry
}

// storageIdentifierWithField builds a binary NAA identifier and overwrites the
// USHORT header field at offset (8: IdentifierSize, 10: NextOffset) with v.
func storageIdentifierWithField(value []byte, offset int, v uint16) []byte {
	entry := storageIdentifier(1, 3, 0, value)
	binary.LittleEndian.PutUint16(entry[offset:offset+2], v)
	return entry
}

func deviceIDDescriptor(entries ...[]byte) []byte {
	size := 12
	for _, entry := range entries {
		size += len(entry)
	}
	descriptor := descriptorWithSize(uint32(size), uint32(len(entries)), nil)
	for i, entry := range entries {
		entry = append([]byte(nil), entry...)
		if i+1 < len(entries) {
			binary.LittleEndian.PutUint16(entry[10:12], uint16(len(entry)))
		} else {
			binary.LittleEndian.PutUint16(entry[10:12], 0)
		}
		descriptor = append(descriptor, entry...)
	}
	return descriptor
}

func descriptorWithSize(size, count uint32, payload []byte) []byte {
	descriptor := make([]byte, 12, 12+len(payload))
	binary.LittleEndian.PutUint32(descriptor[4:8], size)
	binary.LittleEndian.PutUint32(descriptor[8:12], count)
	return append(descriptor, payload...)
}
