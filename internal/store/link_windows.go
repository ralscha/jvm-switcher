//go:build windows

package store

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const (
	fsctlSetReparsePoint   = 0x000900A4
	ioReparseTagMountPoint = 0xA0000003
	maxReparseDataLength   = 16 * 1024
)

// createDirectoryLink creates an NTFS junction, which unlike a symbolic link needs no special privilege.
func createDirectoryLink(link, target string) error {
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	buffer, err := mountPointReparseData(absoluteTarget)
	if err != nil {
		return err
	}
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	if err := setMountPoint(link, buffer); err != nil {
		os.Remove(link)
		return err
	}
	return nil
}

func setMountPoint(link string, buffer []byte) error {
	handle, err := openReparsePoint(link)
	if err != nil {
		return fmt.Errorf("open %s: %w", link, err)
	}
	defer syscall.CloseHandle(handle)

	var returned uint32
	if err := syscall.DeviceIoControl(handle, fsctlSetReparsePoint, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("set reparse point on %s: %w", link, err)
	}
	return nil
}

func openReparsePoint(path string) (syscall.Handle, error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	return syscall.CreateFile(
		pathPointer,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

// mountPointReparseData builds a REPARSE_DATA_BUFFER holding an IO_REPARSE_TAG_MOUNT_POINT payload.
func mountPointReparseData(target string) ([]byte, error) {
	if filepath.VolumeName(target) == "" || strings.HasPrefix(target, `\\`) {
		return nil, fmt.Errorf("JDK store path %q must be a local path with a drive letter", target)
	}
	substituteName := utf16.Encode([]rune(`\??\` + target + "\x00"))
	printName := utf16.Encode([]rune(target + "\x00"))

	const headerLength = 8     // ReparseTag, ReparseDataLength and Reserved
	const nameFieldsLength = 8 // the substitute and print name offsets and lengths
	nameBytes := (len(substituteName) + len(printName)) * 2
	if headerLength+nameFieldsLength+nameBytes > maxReparseDataLength {
		return nil, fmt.Errorf("JDK store path %q is too long for a junction", target)
	}

	buffer := make([]byte, headerLength+nameFieldsLength+nameBytes)
	binary.LittleEndian.PutUint32(buffer[0:], ioReparseTagMountPoint)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(nameFieldsLength+nameBytes))
	binary.LittleEndian.PutUint16(buffer[8:], 0)
	binary.LittleEndian.PutUint16(buffer[10:], uint16(len(substituteName)*2-2))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(len(substituteName)*2))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(len(printName)*2-2))

	names := buffer[headerLength+nameFieldsLength:]
	for index, unit := range substituteName {
		binary.LittleEndian.PutUint16(names[index*2:], unit)
	}
	names = names[len(substituteName)*2:]
	for index, unit := range printName {
		binary.LittleEndian.PutUint16(names[index*2:], unit)
	}
	return buffer, nil
}
