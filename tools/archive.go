//go:build ignore

// archive creates a .zip or .tar.gz file with the given files at its root.
// Usage: go run tools/archive.go <output.zip|output.tar.gz> <files...>
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: archive <output.zip|output.tar.gz> <files...>")
		os.Exit(2)
	}
	out, files := os.Args[1], os.Args[2:]

	var err error
	switch {
	case strings.HasSuffix(out, ".zip"):
		err = writeZip(out, files)
	case strings.HasSuffix(out, ".tar.gz"):
		err = writeTarGz(out, files)
	default:
		err = fmt.Errorf("unsupported archive type: %s", out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeZip(out string, files []string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, name := range files {
		if err := addToZip(zw, name); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

func addToZip(zw *zip.Writer, name string) error {
	src, err := os.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(name)
	hdr.Method = zip.Deflate

	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}

func writeTarGz(out string, files []string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, name := range files {
		if err := addToTar(tw, name); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	return f.Close()
}

func addToTar(tw *tar.Writer, name string) error {
	src, err := os.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(name)

	// Windows has no executable bit, so derive the mode from the file
	// contents to get the same archive on every OS.
	exec, err := isExecutable(src)
	if err != nil {
		return err
	}
	if exec {
		hdr.Mode = 0o755
	} else {
		hdr.Mode = 0o644
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, src)
	return err
}

// executableMagics are the leading bytes of ELF and Mach-O binaries.
var executableMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},    // ELF
	{0xfe, 0xed, 0xfa, 0xce}, // Mach-O 32-bit, big endian
	{0xfe, 0xed, 0xfa, 0xcf}, // Mach-O 64-bit, big endian
	{0xce, 0xfa, 0xed, 0xfe}, // Mach-O 32-bit, little endian
	{0xcf, 0xfa, 0xed, 0xfe}, // Mach-O 64-bit, little endian
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O universal
}

// isExecutable reports whether f starts with an executable header and
// rewinds f to the beginning.
func isExecutable(f io.ReadSeeker) (bool, error) {
	magic := make([]byte, 4)
	n, err := io.ReadFull(f, magic)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if n < len(magic) {
		return false, nil
	}
	for _, m := range executableMagics {
		if bytes.Equal(magic, m) {
			return true, nil
		}
	}
	return false, nil
}
