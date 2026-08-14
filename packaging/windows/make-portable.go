// Command make-portable creates the permanent Windows portable ZIP layout.
package main

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z._-]*$`)

func main() {
	executable := flag.String("executable", "", "signed GoFlasher executable")
	version := flag.String("version", "", "release version")
	output := flag.String("output", "", "output directory")
	flag.Parse()
	if err := packagePortable(*executable, *version, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packagePortable(executable, version, output string) error {
	if executable == "" || output == "" || !versionPattern.MatchString(version) {
		return errors.New("usage: go run ./packaging/windows --executable EXE --version VERSION --output DIR")
	}
	repo, err := filepath.Abs(filepath.Join("packaging", "windows", "..", ".."))
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	name := "GoFlasher-" + version + "-windows-amd64"
	stage, archive := filepath.Join(output, name), filepath.Join(output, name+".zip")
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	_ = os.Remove(archive)
	_ = os.Remove(archive + ".sha256")
	if err := os.MkdirAll(filepath.Join(stage, "licenses"), 0o755); err != nil {
		return err
	}
	if err := copyFile(executable, filepath.Join(stage, "GoFlasher.exe")); err != nil {
		return err
	}
	for _, notice := range []string{"THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.zh-TW.md"} {
		if err := copyFile(filepath.Join(repo, "docs", "legal", notice), filepath.Join(stage, notice)); err != nil {
			return err
		}
	}
	readme, err := os.ReadFile(filepath.Join(repo, "packaging", "windows", "README-Windows.txt"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "README-Windows.txt"), []byte(strings.ReplaceAll(string(readme), "VERSION", version)), 0o644); err != nil {
		return err
	}
	if err := collectLicenses(repo, stage); err != nil {
		return err
	}
	if err := zipTree(output, stage, archive); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	line := hex.EncodeToString(h.Sum(nil)) + "  " + filepath.Base(archive) + "\n"
	return os.WriteFile(archive+".sha256", []byte(line), 0o644)
}

func collectLicenses(repo, stage string) error {
	cmd := exec.Command("go", "list", "-deps", "-tags", "fyne", "-f", `{{with .Module}}{{if .Dir}}{{.Path}}|{{.Dir}}{{end}}{{end}}`, "./cmd/usbwriter")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list compiled modules: %w", err)
	}
	modules := make(map[string]string)
	scan := bufio.NewScanner(strings.NewReader(string(out)))
	for scan.Scan() {
		parts := strings.SplitN(scan.Text(), "|", 2)
		if len(parts) == 2 {
			modules[parts[0]] = parts[1]
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	licenseName := regexp.MustCompile(`^(LICENSE|COPYING|NOTICE)(\..*)?$`)
	for _, module := range paths {
		entries, err := os.ReadDir(modules[module])
		if err != nil {
			return err
		}
		found := false
		for _, entry := range entries {
			if entry.Type().IsRegular() && licenseName.MatchString(entry.Name()) {
				found = true
				safe := regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(module, "_")
				if err := copyFile(filepath.Join(modules[module], entry.Name()), filepath.Join(stage, "licenses", safe+"_"+entry.Name())); err != nil {
					return err
				}
			}
		}
		if !found {
			return fmt.Errorf("compiled module %s has no root license file", module)
		}
	}
	return nil
}

func zipTree(base, stage, destination string) error {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(stage, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
