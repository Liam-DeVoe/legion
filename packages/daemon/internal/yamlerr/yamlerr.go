// Package yamlerr is the refusal of a YAML file the daemon cannot decode, in fixed words: the file
// and the line, never yaml.v3's own text. That text quotes the document. A TypeError quotes the
// scalar it could not decode (whole up to ten characters, its first seven past that), and a
// reference to an anchor the file never set names the anchor, which is any value written after a
// "*". A credential in the file is such a scalar, or such a value, so a file that can hold one
// is refused through here.
package yamlerr

import (
	"fmt"
	"regexp"
)

// lineNumber is yaml.v3's own position in its error, `line <n>`, which comes before any quoted
// text in every error it returns.
var lineNumber = regexp.MustCompile(`\bline ([0-9]+)\b`)

// Refusal is file's refusal for err, a yaml.v3 decode error: the first line err names, or none
// when it names none, as an unknown anchor's error does not.
func Refusal(file string, err error) error {
	if match := lineNumber.FindStringSubmatch(err.Error()); match != nil {
		return fmt.Errorf("%s cannot be read at line %s: it does not parse as YAML, or holds a value of another kind there than the file takes", file, match[1])
	}
	return fmt.Errorf("%s cannot be read: it does not parse as YAML", file)
}
