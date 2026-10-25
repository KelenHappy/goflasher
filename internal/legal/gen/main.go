// Command gen refreshes internal/legal/texts from the modules actually
// compiled into the GUI binary. The texts are committed so that `go build`
// never depends on the module cache being populated; CI re-runs this and fails
// on any diff, which is what stops a new dependency from shipping unnoticed.
package main

import (
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
	return generator{repo: repo, texts: filepath.Join(repo, "internal", "legal", "texts")}.generate()
}

// generator stages the embedded texts of the repository at repo into texts,
// replacing whatever texts held before.
type generator struct {
	repo, texts string
}

func (g generator) modulesDir() string { return filepath.Join(g.texts, "modules") }

func (g generator) generate() error {
	if err := os.RemoveAll(g.texts); err != nil {
		return err
	}
	if err := os.MkdirAll(g.modulesDir(), 0o755); err != nil {
		return err
	}
	if err := g.stageProjectTexts(); err != nil {
		return err
	}
	modules, err := g.compiledModuleDirs()
	if err != nil {
		return err
	}
	names, err := g.stageModuleTexts(modules)
	if err != nil {
		return err
	}
	goroot, err := g.stageGoLicense()
	if err != nil {
		return err
	}
	return g.writeIndex(append(names, goroot))
}

func repoRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", selfModule).Output()
	if err != nil {
		return "", fmt.Errorf("locate module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// stageProjectTexts copies the texts that belong to GoFlasher itself.
func (g generator) stageProjectTexts() error {
	sources := map[string]string{
		"LICENSE":                      filepath.Join(g.repo, "LICENSE"),
		"THIRD_PARTY_NOTICES.md":       filepath.Join(g.repo, "docs", "legal", "THIRD_PARTY_NOTICES.md"),
		"THIRD_PARTY_NOTICES.zh-TW.md": filepath.Join(g.repo, "docs", "legal", "THIRD_PARTY_NOTICES.zh-TW.md"),
	}
	for name, source := range sources {
		if err := copyFile(source, filepath.Join(g.texts, name)); err != nil {
			return err
		}
	}
	return nil
}

// releaseTarget is a GOOS value the GUI is released for.
type releaseTarget string

// releaseTargets are the platforms whose GUI builds share these embedded texts.
// Listing each one keeps a platform-only dependency (such as PureGo on macOS)
// in the notices even when the generator runs on another host.
var releaseTargets = []releaseTarget{"linux", "darwin", "windows"}

// compiledModuleDirs maps every module compiled into the GUI binary for any
// release target to its source directory, excluding the repository's own module.
func (g generator) compiledModuleDirs() (map[string]string, error) {
	modules := make(map[string]string)
	for _, target := range releaseTargets {
		if err := g.addCompiledModules(modules, target); err != nil {
			return nil, err
		}
	}
	return modules, nil
}

// addCompiledModules adds the modules the GUI binary compiles for target.
func (g generator) addCompiledModules(modules map[string]string, target releaseTarget) error {
	cmd := exec.Command("go", "list", "-deps", "-tags", "fyne,wimlib", "-f", `{{with .Module}}{{if .Dir}}{{.Path}}|{{.Dir}}{{end}}{{end}}`, "./cmd/usbwriter")
	cmd.Dir = g.repo
	cmd.Env = append(os.Environ(), "GOOS="+string(target), "CGO_ENABLED=1")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list compiled modules for %s: %w", target, err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		path, dir, ok := strings.Cut(line, "|")
		if ok && path != selfModule {
			modules[path] = dir
		}
	}
	return nil
}

// stageModuleTexts copies each module's root license files and returns the
// index entries describing them.
func (g generator) stageModuleTexts(modules map[string]string) ([]indexEntry, error) {
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	entries := make([]indexEntry, 0, len(paths))
	for _, path := range paths {
		files, err := g.copyModuleLicenses(module{path: path, dir: modules[path]})
		if err != nil {
			return nil, err
		}
		entries = append(entries, indexEntry{title: path, files: files})
	}
	return entries, nil
}

// module is a compiled dependency: its module path and source directory.
type module struct {
	path, dir string
}

// copyModuleLicenses copies every root license file of m and fails when the
// module ships none, so an unlicensed dependency cannot reach a release.
func (g generator) copyModuleLicenses(m module) ([]string, error) {
	listing, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	safe := unsafeNameChars.ReplaceAllString(m.path, "_")
	var files []string
	for _, entry := range listing {
		if !entry.Type().IsRegular() || !licenseNamePattern.MatchString(entry.Name()) {
			continue
		}
		name := safe + "_" + entry.Name()
		if err := copyFile(filepath.Join(m.dir, entry.Name()), filepath.Join(g.modulesDir(), name)); err != nil {
			return nil, err
		}
		files = append(files, "modules/"+name)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("compiled module %s has no root license file", m.path)
	}
	sort.Strings(files)
	return files, nil
}

// stageGoLicense copies the Go project's license, which covers the runtime
// derived portions of PureGo as well as the standard library.
func (g generator) stageGoLicense() (indexEntry, error) {
	source, err := goLicensePath()
	if err != nil {
		return indexEntry{}, err
	}
	name := "modules/golang.org_go_LICENSE"
	if err := copyFile(source, filepath.Join(g.texts, name)); err != nil {
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
func (g generator) writeIndex(entries []indexEntry) error {
	var b strings.Builder
	b.WriteString("# Generated by internal/legal/gen. Do not edit.\n")
	for _, entry := range entries {
		b.WriteString(entry.title + "\t" + strings.Join(entry.files, ",") + "\n")
	}
	return os.WriteFile(filepath.Join(g.texts, "index.txt"), []byte(b.String()), 0o644)
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}
