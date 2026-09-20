// Package legal serves the license texts that must accompany the program.
//
// GPL-3.0 and LGPL-2.1 require the license texts to reach the user with the
// binary, not merely as a link, so the texts are embedded here and shown in
// settings. Packages keep their own copies as well: Debian policy requires
// /usr/share/doc/<package>/copyright, and an installed file stays readable
// without launching the GUI.
package legal

import (
	"embed"
	"path"
	"strings"
)

//go:generate go run ./gen

//go:embed texts
var texts embed.FS

// Document is one component's notices as shown in the settings dialog. Body
// holds the concatenated text of every license file the component ships.
type Document struct {
	Title string
	Body  string
}

// projectDocuments are shown before the dependency list; they describe
// GoFlasher itself and carry the third-party notices, including the current
// status of components such as wimlib that are not Go modules.
var projectDocuments = []struct{ title, file string }{
	{"GoFlasher (GPL-3.0-or-later)", "LICENSE"},
	{"Third-party notices", "THIRD_PARTY_NOTICES.md"},
	{"第三方元件聲明（繁體中文）", "THIRD_PARTY_NOTICES.zh-TW.md"},
}

// Documents returns every embedded notice in display order. It never returns
// an error: the texts are embedded at build time, so a missing file is a build
// failure rather than a runtime condition.
func Documents() []Document {
	documents := make([]Document, 0, len(projectDocuments)+32)
	for _, entry := range projectDocuments {
		documents = append(documents, Document{Title: entry.title, Body: read(entry.file)})
	}
	return append(documents, moduleDocuments()...)
}

// moduleDocuments parses the generated index, which fixes both the order of
// components and the grouping of modules that ship several license files.
func moduleDocuments() []Document {
	index := read("index.txt")
	documents := make([]Document, 0, strings.Count(index, "\n"))
	for line := range strings.SplitSeq(index, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		title, files, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		bodies := make([]string, 0, 2)
		for file := range strings.SplitSeq(files, ",") {
			bodies = append(bodies, read(file))
		}
		documents = append(documents, Document{Title: title, Body: strings.Join(bodies, "\n\n")})
	}
	return documents
}

func read(name string) string {
	data, err := texts.ReadFile(path.Join("texts", name))
	if err != nil {
		return ""
	}
	return string(data)
}
