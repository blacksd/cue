// Copyright 2025 The CUE Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package koala

import (
	"fmt"
	"io"
	"strings"

	"cuelang.org/go/cue"
)

// Encoder writes CUE values as XML using the koala encoding.
type Encoder struct {
	writer io.Writer
}

// NewEncoder creates an encoder that writes XML to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{writer: w}
}

// Encode writes v as a koala-encoded XML document.
// The value must be a struct with exactly one field, whose label
// becomes the root XML element name.
func (enc *Encoder) Encode(v cue.Value) error {
	// Emit XML declaration.
	if _, err := fmt.Fprint(enc.writer, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"); err != nil {
		return err
	}

	// The top-level value must be a struct with exactly one field (the root element).
	if v.Kind() != cue.StructKind {
		return fmt.Errorf("koala: top-level value must be a struct, got %v", v.Kind())
	}
	iter, err := v.Fields()
	if err != nil {
		return fmt.Errorf("koala: iterating top-level fields: %w", err)
	}
	if !iter.Next() {
		return fmt.Errorf("koala: top-level struct has no fields (need exactly one root element)")
	}
	rootName := iter.Selector().Unquoted()
	rootVal := iter.Value()
	if iter.Next() {
		return fmt.Errorf("koala: top-level struct has multiple fields (XML requires exactly one root element)")
	}

	return enc.encodeValue(rootName, rootVal, 0)
}

// encodeValue dispatches encoding based on the CUE value kind.
func (enc *Encoder) encodeValue(name string, v cue.Value, depth int) error {
	switch v.Kind() {
	case cue.StructKind:
		return enc.encodeStruct(name, v, depth)
	case cue.ListKind:
		return enc.encodeList(name, v, depth)
	case cue.StringKind, cue.IntKind, cue.FloatKind, cue.BoolKind:
		return enc.encodeScalar(name, v, depth)
	default:
		return fmt.Errorf("koala: unsupported value kind %v for element %q", v.Kind(), name)
	}
}

// encodeStruct writes an XML element from a koala-convention CUE struct.
// Fields prefixed with "$" become XML attributes, "$$" becomes text content,
// and all other fields become child elements.
func (enc *Encoder) encodeStruct(name string, v cue.Value, depth int) error {
	type field struct {
		name  string
		value cue.Value
	}

	var attrs []field
	var children []field
	var textContent *string

	iter, err := v.Fields()
	if err != nil {
		return err
	}
	for iter.Next() {
		label := iter.Selector().Unquoted()
		val := iter.Value()

		if label == contentAttribute {
			s, err := valueToStr(val)
			if err != nil {
				return fmt.Errorf("koala: converting text content to string: %w", err)
			}
			textContent = &s
		} else if strings.HasPrefix(label, attributeSymbol) {
			attrs = append(attrs, field{name: label, value: val})
		} else {
			children = append(children, field{name: label, value: val})
		}
	}

	indent := strings.Repeat("\t", depth)

	// Build start tag with attributes.
	if _, err := fmt.Fprintf(enc.writer, "%s<%s", indent, name); err != nil {
		return err
	}
	for _, a := range attrs {
		attrName := a.name[len(attributeSymbol):]
		attrVal, err := valueToStr(a.value)
		if err != nil {
			return fmt.Errorf("koala: converting attribute %q to string: %w", attrName, err)
		}
		if _, err := fmt.Fprintf(enc.writer, " %s=\"%s\"", attrName, escapeAttr(attrVal)); err != nil {
			return err
		}
	}

	// Self-closing tag: no text content and no children.
	if textContent == nil && len(children) == 0 {
		_, err := fmt.Fprint(enc.writer, "/>\n")
		return err
	}

	// Inline text content (no child elements).
	if textContent != nil && len(children) == 0 {
		_, err := fmt.Fprintf(enc.writer, ">%s</%s>\n", escapeText(*textContent), name)
		return err
	}

	// Element with children.
	if _, err := fmt.Fprint(enc.writer, ">\n"); err != nil {
		return err
	}
	for _, child := range children {
		if err := enc.encodeValue(child.name, child.value, depth+1); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(enc.writer, "%s</%s>\n", indent, name)
	return err
}

// encodeList writes repeated XML elements with the same tag name,
// one per list item.
func (enc *Encoder) encodeList(name string, v cue.Value, depth int) error {
	iter, err := v.List()
	if err != nil {
		return err
	}
	for iter.Next() {
		if err := enc.encodeValue(name, iter.Value(), depth); err != nil {
			return err
		}
	}
	return nil
}

// encodeScalar writes a leaf XML element containing a stringified scalar value.
func (enc *Encoder) encodeScalar(name string, v cue.Value, depth int) error {
	s, err := valueToStr(v)
	if err != nil {
		return err
	}
	indent := strings.Repeat("\t", depth)
	_, err = fmt.Fprintf(enc.writer, "%s<%s>%s</%s>\n", indent, name, escapeText(s), name)
	return err
}

// valueToStr converts a CUE scalar value to its string representation for XML output.
func valueToStr(v cue.Value) (string, error) {
	switch v.Kind() {
	case cue.StringKind:
		return v.String()
	case cue.BoolKind:
		b, err := v.Bool()
		if err != nil {
			return "", err
		}
		if b {
			return "true", nil
		}
		return "false", nil
	case cue.IntKind:
		i, err := v.Int64()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", i), nil
	case cue.FloatKind:
		d, _ := v.Decimal()
		return d.String(), nil
	default:
		return "", fmt.Errorf("koala: cannot convert %v to string", v.Kind())
	}
}

// escapeText escapes special XML characters in text content.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// escapeAttr escapes special XML characters in attribute values.
func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
