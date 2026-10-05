// pdfref.go reads which indirect object a value refers to from the text form
// ledongthuc/pdf prints for the dictionary or array that holds it.

package extract

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

// objRef names an indirect object by its number and generation, as a
// reference "id gen R" does. The zero objRef names no object: it is what the
// trailer's values are written in.
type objRef struct {
	id  uint32
	gen uint16
}

// refTextRE matches a reference at the start of a value's text form, up to
// the end of the R. What follows it is a space before the next value, or the
// end of the dictionary or array, never a letter.
var refTextRE = regexp.MustCompile(`^(\d+) (\d+) R\b`)

// leadingRef reads the reference text starts with, and returns it with the
// length of its text, or a length of 0 when text does not start with one. A
// number or generation too large for a reference, which the reader does not
// read as one either, is no reference.
func leadingRef(text string) (ref objRef, n int) {
	m := refTextRE.FindStringSubmatch(text)
	if m == nil {
		return objRef{}, 0
	}
	id, idErr := strconv.ParseUint(m[1], 10, 32)
	gen, genErr := strconv.ParseUint(m[2], 10, 16)
	if idErr != nil || genErr != nil {
		return objRef{}, 0
	}
	return objRef{id: uint32(id), gen: uint16(gen)}, len(m[0])
}

// dictRefs returns the object each key of the dictionary v refers to, for
// every key whose value is an indirect reference.
//
// The reader resolves a reference as it is read and keeps no object number on
// the value it returns, but Value.String prints a reference it has not
// resolved as "id gen R", in a dictionary's text as anywhere else. The text is
// followed key by key: the keys come in the order Value.Keys gives, which is
// the order the text prints them in, and a value that is not a reference is
// passed over by the text it prints on its own. So no value's text, a title
// holding "/Next 9 0 R" or a name holding a space, is ever read as a key.
func dictRefs(v pdf.Value) map[string]objRef {
	keys := v.Keys()
	labels := make([]string, len(keys))
	for i, key := range keys {
		labels[i] = "/" + key + " "
	}
	at := valueRefs(v.String(), "<<", labels, func(i int) string { return v.Key(keys[i]).String() })
	refs := make(map[string]objRef, len(at))
	for i, ref := range at {
		refs[keys[i]] = ref
	}
	return refs
}

// arrayRefs returns the object each element of the array v refers to, by its
// index, for every element that is an indirect reference. It reads v's text
// form as dictRefs does.
func arrayRefs(v pdf.Value) map[int]objRef {
	return valueRefs(v.String(), "[", make([]string, v.Len()), func(i int) string { return v.Index(i).String() })
}

// valueRefs follows text, the text form of a dictionary or an array, which
// opens with open and then prints each value after its label, a space between
// one and the next. It records, by position, the object each value that is a
// reference refers to, and passes over any other value by the text direct
// returns for it. It stops where the text is not what the values print, and
// keeps what it read before that.
func valueRefs(text, open string, labels []string, direct func(int) string) map[int]objRef {
	refs := map[int]objRef{}
	rest, ok := strings.CutPrefix(text, open)
	for i := 0; ok && i < len(labels); i++ {
		lead := labels[i]
		if i > 0 {
			lead = " " + lead
		}
		if rest, ok = strings.CutPrefix(rest, lead); !ok {
			break
		}
		if ref, n := leadingRef(rest); n > 0 {
			refs[i], rest = ref, rest[n:]
			continue
		}
		rest, ok = strings.CutPrefix(rest, direct(i))
	}
	return refs
}
