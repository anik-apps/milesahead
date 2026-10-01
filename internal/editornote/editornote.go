// Package editornote separates editor notes from article content.
//
// An editor note is an element with the class "editor-note": a remark meant
// for the admin reviewing an article, not for readers ("Could not find a link
// for: ..."). In March 2026 the revision prompt asked the model to append one
// when it could not carry out a request, and the stylesheet hid the class on
// the public site. Hiding is not removing: the text was still in the page's
// HTML, the search index and its snippets, and anything else built from the
// content. The prompt no longer asks for notes, but a model can still write
// one.
//
// So notes are split out of content before it is stored and kept in their own
// admin-only field, and everything that reads content strips any that are
// still there, using this package.
package editornote

import (
	"bytes"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// Class marks an editor note.
const Class = "editor-note"

// Split removes every element carrying the editor-note class from content,
// along with everything inside it, and returns the content without them and
// the notes' text, one note per line. Content without notes is returned
// unchanged; otherwise everything outside the notes is kept byte for byte,
// apart from surrounding whitespace.
//
// A note left unclosed runs to the end of the content, which is how a browser
// would have hidden it.
func Split(content string) (clean, notes string) {
	// The class name must appear literally for an element to carry it (a
	// character reference in the attribute is not something the model
	// writes), so the common case skips the tokenizer.
	if !strings.Contains(content, Class) {
		return content, ""
	}

	z := html.NewTokenizer(strings.NewReader(content))
	var out bytes.Buffer
	var found []string
	var text strings.Builder

	inNote := false
	noteTag := ""
	depth := 0 // open noteTag elements inside the current note, itself included
	endNote := func() {
		if t := strings.Join(strings.Fields(text.String()), " "); t != "" {
			found = append(found, t)
		}
		text.Reset()
		inNote = false
	}

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() != io.EOF {
				// The tokenizer only fails on read errors, which a string
				// reader does not produce; keep the content as it was rather
				// than risk dropping any of it.
				return content, ""
			}
			break
		}
		raw := append([]byte(nil), z.Raw()...)

		if !inNote {
			if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
				name, hasClass := tagHasNoteClass(z)
				if hasClass {
					if tt == html.SelfClosingTagToken || isVoid(name) {
						continue // an empty note: drop it
					}
					inNote, noteTag, depth = true, name, 1
					continue
				}
			}
			out.Write(raw)
			continue
		}

		switch tt {
		case html.StartTagToken:
			if name, _ := z.TagName(); string(name) == noteTag {
				depth++
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == noteTag {
				depth--
				if depth == 0 {
					endNote()
					continue
				}
			}
		case html.TextToken:
			text.WriteString(html.UnescapeString(string(raw)))
			text.WriteByte(' ')
		}
	}
	if inNote {
		endNote()
	}

	if len(found) == 0 && out.Len() == len(content) {
		// "editor-note" appeared only as text, not as a class.
		return content, ""
	}
	return strings.TrimSpace(out.String()), Join(found...)
}

// Strip returns content without its editor notes.
func Strip(content string) string {
	clean, _ := Split(content)
	return clean
}

// Join combines note texts, one per line, dropping blanks and repeats.
func Join(notes ...string) string {
	var lines []string
	seen := map[string]bool{}
	for _, n := range notes {
		for _, line := range strings.Split(n, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// Lines splits stored notes into one entry per note, for display.
func Lines(notes string) []string {
	if j := Join(notes); j != "" {
		return strings.Split(j, "\n")
	}
	return nil
}

// tagHasNoteClass reports the current tag's name and whether its class
// attribute lists the editor-note class.
func tagHasNoteClass(z *html.Tokenizer) (string, bool) {
	name, more := z.TagName()
	tag := string(name)
	for more {
		var key, val []byte
		key, val, more = z.TagAttr()
		if string(key) != "class" {
			continue
		}
		for _, c := range strings.Fields(string(val)) {
			if c == Class {
				return tag, true
			}
		}
	}
	return tag, false
}

// isVoid reports whether an element never has content or an end tag.
func isVoid(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "source", "track", "wbr":
		return true
	}
	return false
}
