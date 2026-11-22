package leakguard

import "strings"

// objectLinks is what one object's header says beside its records: a commit's parents, whose
// number says how its diff is read (diffTree); the object a tag points to, which can be a tag the
// push sends too; and a commit's encoding, where it names one, which says whether its text is the
// UTF-8 the rules match (Guard.inUTF8).
type objectLinks struct {
	parents  int
	object   string
	encoding string
}

// objectRecords hands emit one commit or tag, as git stores it (`git cat-file commit` or `git
// cat-file tag`, which raw is), as records whose labels start with label: one for each identity
// (author, committer, tagger) by name and address, without the time and zone after them; one for
// each other field of the header, its name included (a tag's name, an encoding, a field git does
// not know); and one for the message. A merge commit's mergetag field is the whole of a signed tag
// the merge was made from, which the push carries inside the commit, so it is read as a tag of its
// own under "<label> mergetag".
//
// Two things are not read. A tree, parent or object field that holds one object id names no text
// of anyone's. A signature's bytes (unsigned) are base64, in which a rule that matches a short word
// in any case would now and then find one by chance: a gpgsig field's, and a tag's, whose
// signature git writes at the end of its message. tag says raw is a tag.
//
// The object is held whole, as one commit's message always was; nothing of the push's diff is.
func objectRecords(label, raw string, tag bool, emit func(Record) error) (objectLinks, error) {
	var links objectLinks
	// The header ends at the first blank line. A field's further lines each start with a space, a
	// blank one included, so no field holds one.
	head, message, _ := strings.Cut(raw, "\n\n")
	for _, f := range headerFields(head) {
		var record Record
		switch {
		case (f.name == "tree" || f.name == "parent" || f.name == "object") && objectID(f.value):
			if f.name == "parent" {
				links.parents++
			}
			if f.name == "object" {
				links.object = f.value
			}
			continue
		case f.name == "author" || f.name == "committer" || f.name == "tagger":
			record = Record{Label: label + " " + f.name, Text: identity(f.value)}
		case f.name == "mergetag":
			if _, err := objectRecords(label+" mergetag", f.value, true, emit); err != nil {
				return links, err
			}
			continue
		case f.name == "gpgsig" || f.name == "gpgsig-sha256":
			record = Record{Label: label + " header", Text: f.name + " " + unsigned(f.value)}
		default:
			if f.name == "encoding" {
				links.encoding = f.value
			}
			record = Record{Label: label + " header", Text: f.name + " " + f.value}
		}
		if err := emit(record); err != nil {
			return links, err
		}
	}
	if tag {
		message = unsigned(message)
	}
	return links, emit(Record{Label: label + " message", Text: message})
}

// utf8Name is whether name, a commit's encoding field, names UTF-8 in either spelling git takes
// for it, in any case: a commit that names it is UTF-8 already, as one that names no encoding is.
func utf8Name(name string) bool {
	return strings.EqualFold(name, "UTF-8") || strings.EqualFold(name, "UTF8")
}

// field is one field of an object's header: its name, and its value, a value of several lines
// with its lines joined by line breaks and the space git writes before each line after the first
// taken off.
type field struct {
	name  string
	value string
}

// headerFields is head, an object's header without the blank line that ends it, as its fields.
func headerFields(head string) []field {
	var fields []field
	for line := range strings.SplitSeq(head, "\n") {
		if rest, more := strings.CutPrefix(line, " "); more && len(fields) > 0 {
			fields[len(fields)-1].value += "\n" + rest
			continue
		}
		if line == "" {
			continue
		}
		name, value, _ := strings.Cut(line, " ")
		fields = append(fields, field{name: name, value: value})
	}
	return fields
}

// identity is an author's, committer's or tagger's field without the time and zone git writes
// after the address: the name and the address, which is what a commit's identity record has
// always held.
func identity(value string) string {
	if end := strings.LastIndexByte(value, '>'); end >= 0 {
		return value[:end+1]
	}
	return value
}

// objectID is whether value is one object id, as long as either of git's hashes writes one.
func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range []byte(value) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// signatureStarts are the lines git starts a signature with, gpg's, x509's and ssh's: the ones
// git itself looks for at the end of a tag's message to find its signature.
var signatureStarts = []string{
	"-----BEGIN PGP SIGNATURE-----",
	"-----BEGIN PGP MESSAGE-----",
	"-----BEGIN SIGNED MESSAGE-----",
	"-----BEGIN SSH SIGNATURE-----",
}

// unsigned is text without the bytes of a signature it holds: within each block from a line that
// starts a signature (signatureStarts) to the next line that starts "-----END ", that first line,
// the last, and each line of base64 alone, a blank one included. Any other line of the block (an
// armour header such as "Comment: …", or text a hand-made object put there) is kept and read, as
// is every line outside a block. Text with no signature comes back as it was, uncopied.
func unsigned(text string) string {
	if !strings.Contains(text, "-----BEGIN ") {
		return text
	}
	var kept strings.Builder
	inside, first := false, true
	for line := range strings.SplitSeq(text, "\n") {
		switch {
		case !inside && startsSignature(line):
			inside = true
			continue
		case inside && strings.HasPrefix(line, "-----END "):
			inside = false
			continue
		case inside && base64Line(line):
			continue
		}
		if !first {
			kept.WriteByte('\n')
		}
		first = false
		kept.WriteString(line)
	}
	return kept.String()
}

// startsSignature is whether line is one a signature starts with.
func startsSignature(line string) bool {
	for _, start := range signatureStarts {
		if strings.HasPrefix(line, start) {
			return true
		}
	}
	return false
}

// base64Line is whether line holds nothing but base64's characters, its padding included.
func base64Line(line string) bool {
	for _, c := range []byte(line) {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}
