// Command gen refreshes internal/legal/texts from the modules actually
// compiled into the GUI binary. The texts are committed so that `go build`
// never depends on the module cache being populated; CI re-runs this and fails
// on any diff, which is what stops a new dependency from shipping unnoticed.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	licenseNamePattern = regexp.MustCompile(`^(LICENSE|COPYING|NOTICE)(\..*)?$`)
	unsafeNameChars    = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// selfModule is the repository's own module path; its license is staged from
// the repository root rather than from a dependency directory.
const selfModule = "github.com/goflasher/goflasher"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	repo, err := repoRoot()
	if err != nil {
		return err
	}
	texts := filepath.Join(repo, "internal", "legal", "texts")
	if err := os.RemoveAll(texts); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(texts, "modules"), 0o755); err != nil {
		return err
	}
	if err := stageProjectTexts(repo, texts); err != nil {
		return err
	}
	modules, err := compiledModuleDirs(repo)
	if err != nil {
		return err
	}
	names, err := stageModuleTexts(modules, filepath.Join(texts, "modules"))
	if err != nil {
		return err
	}
	goroot, err := stageGoLicense(texts)
	if err != nil {
		return err
	}
	return writeIndex(texts, append(names, goroot))
}

func repoRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", selfModule).Output()
	if err != nil {
		return "", fmt.Errorf("locate module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// stageProjectTexts copies the texts that belong to GoFlasher itself.
func stageProjectTexts(repo, texts string) error {
	sources := map[string]string{
		"LICENSE":                      filepath.Join(repo, "LICENSE"),
		"THIRD_PARTY_NOTICES.md":       filepath.Join(repo, "docs", "legal", "THIRD_PARTY_NOTICES.md"),
		"THIRD_PARTY_NOTICES.zh-TW.md": filepath.Join(repo, "docs", "legal", "THIRD_PARTY_NOTICES.zh-TW.md"),
	}
	for name, source := range sources {
		if err := copyFile(source, filepath.Join(texts, name)); err != nil {
			return err
		}
	}
	return nil
}

// compiledModuleDirs maps every module compiled into the GUI binary to its
// source directory, excluding the repository's own module.
func compiledModuleDirs(repo string) (map[string]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-tags", "fyne", "-f", `{{with .Module}}{{if .Dir}}{{.Path}}|{{.Dir}}{{end}}{{end}}`, "./cmd/usbwriter")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list compiled modules: %w", err)
	}
	modules := make(map[string]string)
	scan := bufio.NewScanner(strings.NewReader(string(out)))
	for scan.Scan() {
		parts := strings.SplitN(scan.Text(), "|", 2)
		if len(parts) == 2 && parts[0] != selfModule {
			modules[parts[0]] = parts[1]
		}
	}
	return modules, scan.Err()
}

// stageModuleTexts copies each module's root license files and returns the
// index entries describing them.
func stageModuleTexts(modules map[string]string, destination string) ([]indexEntry, error) {
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	entries := make([]indexEntry, 0, len(paths))
	for _, module := range paths {
		files, err := copyModuleLicenses(module, modules[module], destination)
		if err != nil {
			return nil, err
		}
		entries = append(entries, indexEntry{title: module, files: files})
	}
	return entries, nil
}

// copyModuleLicenses copies every root license file of module and fails when
// the module ships none, so an unlicensed dependency cannot reach a release.
func copyModuleLicenses(module, dir, destination string) ([]string, error) {
	listing, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	safe := unsafeNameChars.ReplaceAllString(module, "_")
	var files []string
	for _, entry := range listing {
		if !entry.Type().IsRegular() || !licenseNamePattern.MatchString(entry.Name()) {
			continue
		}
		name := safe + "_" + entry.Name()
		if err := copyFile(filepath.Join(dir, entry.Name()), filepath.Join(destination, name)); err != nil {
			return nil, err
		}
		files = append(files, "modules/"+name)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("compiled module %s has no root license file", module)
	}
	sort.Strings(files)
	return files, nil
}

// stageGoLicense copies the Go project's license, which covers the runtime
// derived portions of PureGo as well as the standard library.
func stageGoLicense(texts string) (indexEntry, error) {
	source, err := goLicensePath()
	if err != nil {
		return indexEntry{}, err
	}
	name := "modules/golang.org_go_LICENSE"
	if err := copyFile(source, filepath.Join(texts, name)); err != nil {
		return indexEntry{}, err
	}
	return indexEntry{title: "The Go Programming Language", files: []string{name}}, nil
}

// goLicensePath locates the Go license, which distributions do not agree on:
// upstream tarballs keep it in GOROOT, while Fedora and Debian move it under
// share/. GO_LICENSE_FILE overrides the search for layouts matching neither.
func goLicensePath() (string, error) {
	if override := os.Getenv("GO_LICENSE_FILE"); override != "" {
		return override, nil
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return "", fmt.Errorf("locate GOROOT: %w", err)
	}
	goroot := strings.TrimSpace(string(out))
	candidates := []string{
		filepath.Join(goroot, "LICENSE"),
		filepath.Join(goroot, "..", "..", "share", "licenses", "golang", "LICENSE"),
		filepath.Join(goroot, "..", "..", "share", "doc", "golang-go", "LICENSE"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("go license not found near %s; set GO_LICENSE_FILE", goroot)
}

// indexEntry is one component as it will appear in the settings dialog.
type indexEntry struct {
	title string
	files []string
}

// writeIndex records the display order and grouping so the embedded texts do
// not have to be re-derived from file names at runtime.
func writeIndex(texts string, entries []indexEntry) error {
	var b strings.Builder
	b.WriteString("# Generated by internal/legal/gen. Do not edit.\n")
	for _, entry := range entries {
		b.WriteString(entry.title + "\t" + strings.Join(entry.files, ",") + "\n")
	}
	return os.WriteFile(filepath.Join(texts, "index.txt"), []byte(b.String()), 0o644)
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}
