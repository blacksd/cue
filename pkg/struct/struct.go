// Copyright 2019 CUE Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package struct defines utilities for struct types.
package structs

import (
	"fmt"
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/token"
	"cuelang.org/go/internal/core/adt"
	"cuelang.org/go/internal/pkg"
	"cuelang.org/go/internal/value"
)

// MinFields validates the minimum number of fields that are part of a struct.
// It can only be used as a validator, for instance `MinFields(3)`.
//
// Only fields that are part of the data model count. This excludes hidden
// fields, optional fields, and definitions.
func MinFields(object pkg.Struct, n int) (bool, error) {
	count := object.Len()
	code := adt.EvalError
	if object.IsOpen() || count+object.NumConstraintFields() >= n {
		code = adt.IncompleteError
	}
	if count < n {
		return false, pkg.ValidationError{B: &adt.Bottom{
			Code: code,
			Err:  errors.Newf(token.NoPos, "len(fields) < MinFields(%[2]d) (%[1]d < %[2]d)", count, n),
		}}
	}
	return true, nil
}

// MaxFields validates the maximum number of fields that are part of a struct.
// It can only be used as a validator, for instance `MaxFields(3)`.
//
// Only fields that are part of the data model count. This excludes hidden
// fields, optional fields, and definitions.
func MaxFields(object pkg.Struct, n int) (bool, error) {
	count := object.Len()
	if count > n {
		return false, pkg.ValidationError{B: &adt.Bottom{
			Code: adt.EvalError,
			Err:  errors.Newf(token.NoPos, "len(fields) > MaxFields(%[2]d) (%[1]d > %[2]d)", count, n),
		}}
	}

	return true, nil
}

// HasAttr returns a copy of s containing only fields that have
// a @<attrName> attribute. Fields without the attribute are excluded.
//
// When an included field's value is a struct, the filter is applied
// recursively: nested fields without the attribute are also excluded.
//
// Optional fields remain optional in the output.
func HasAttr(s pkg.Schema, attrName string) (ast.Expr, error) {
	ctx := value.OpContext(s)
	return hasAttr(ctx, s, attrName)
}

func hasAttr(_ *adt.OpContext, s pkg.Schema, attrName string) (ast.Expr, error) {
	return filterStruct(s, attrName, nil)
}

// FilterByAttr returns a copy of s containing only fields whose
// @<attrName> attribute value matches the given pattern. The attribute
// value is split by "|" (pipe) and each segment is tested against
// the pattern individually.
//
// The pattern is a regular expression. If the pattern does not start
// with "^" or end with "$", it is automatically anchored (wrapped in
// ^...$) so that "customer" matches exactly. To opt into substring
// or partial matching, include explicit anchors, e.g. "^cust" or
// ".*partial.*".
//
// Fields without a @<attrName> attribute are included by default.
// When an included field's value is a struct, the filter is applied
// recursively.
//
// Optional fields remain optional in the output.
func FilterByAttr(s pkg.Schema, attrName, pattern string) (ast.Expr, error) {
	ctx := value.OpContext(s)
	return filterByAttr(ctx, s, attrName, pattern)
}

func filterByAttr(_ *adt.OpContext, s pkg.Schema, attrName, pattern string) (ast.Expr, error) {
	re, err := compilePattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("FilterByAttr: invalid pattern: %w", err)
	}
	return filterStruct(s, attrName, re)
}

// compilePattern compiles a regex pattern, auto-anchoring it if the
// user has not provided explicit ^ or $ anchors.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(pattern, "^") && !strings.HasSuffix(pattern, "$") {
		pattern = "^" + pattern + "$"
	}
	return regexp.Compile(pattern)
}

// filterStruct filters v's fields by attribute. If re is nil (HasAttr mode),
// only fields that carry @attrName are included. If re is non-nil
// (FilterByAttr mode), fields without @attrName are included by default
// and fields with @attrName are included only when a pipe-separated
// segment of the attribute value matches re.
//
// Field values are obtained via Value.Syntax with InlineImports on each
// individual field value, ensuring all cross-package references are
// resolved inline. For struct-typed field values, the resulting AST is
// further filtered recursively.
func filterStruct(v cue.Value, attrName string, re *regexp.Regexp) (*ast.StructLit, error) {
	result := &ast.StructLit{}
	iter, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil, err
	}
	for iter.Next() {
		sel := iter.Selector()
		if !sel.IsString() {
			continue
		}
		fieldVal := iter.Value()

		if !attrMatches(fieldVal, attrName, re) {
			continue
		}

		label := ast.NewIdent(sel.Unquoted())
		val := fieldVal.Syntax(cue.Raw(), cue.InlineImports(true)).(ast.Expr)

		// For struct-typed values, recursively filter the AST by attribute.
		// Only filter actual structs — InlineImports may wrap non-struct
		// values (lists, disjunctions) in a StructLit to hold let clauses
		// for cross-package references; those must be kept intact.
		if sl, ok := val.(*ast.StructLit); ok && fieldVal.IncompleteKind() == cue.StructKind {
			val = filterAST(sl, attrName, re)
		}

		f := &ast.Field{Label: label, Value: val}
		if iter.IsOptional() {
			f.Constraint = token.OPTION
		}
		result.Elts = append(result.Elts, f)
	}
	return result, nil
}

// filterAST walks an ast.StructLit and removes fields that don't match
// the attribute filter. For nested struct values, it recurses.
func filterAST(sl *ast.StructLit, attrName string, re *regexp.Regexp) *ast.StructLit {
	result := &ast.StructLit{}
	for _, elt := range sl.Elts {
		f, ok := elt.(*ast.Field)
		if !ok {
			// Preserve let clauses, ellipsis, comments — they may be
			// referenced by surviving fields.
			result.Elts = append(result.Elts, elt)
			continue
		}
		if !astAttrMatches(f, attrName, re) {
			continue
		}
		nf := &ast.Field{
			Label:      f.Label,
			Value:      f.Value,
			Constraint: f.Constraint,
		}
		if nested, ok := f.Value.(*ast.StructLit); ok {
			nf.Value = filterAST(nested, attrName, re)
		}
		result.Elts = append(result.Elts, nf)
	}
	return result
}

// attrMatches reports whether the field should be included based on
// the cue.Value's attributes.
//
// When re is nil (HasAttr mode): the field is included only if it
// carries the @attrName attribute.
//
// When re is non-nil (FilterByAttr mode): the field is included if
// it has no @attrName attribute (unannotated = include-all default),
// or if any pipe-separated segment of the attribute value matches re.
func attrMatches(v cue.Value, attrName string, re *regexp.Regexp) bool {
	attr := v.Attribute(attrName)
	hasAttr := attr.Err() == nil

	if re == nil {
		return hasAttr
	}
	if !hasAttr {
		return true
	}
	for i := range attr.NumArgs() {
		key, val := attr.Arg(i)
		actual := val
		if val == "" {
			actual = key
		}
		for _, seg := range strings.Split(actual, "|") {
			if re.MatchString(strings.TrimSpace(seg)) {
				return true
			}
		}
	}
	return false
}

// astAttrMatches reports whether an AST field should be included based
// on its attributes. It mirrors the semantics of attrMatches but works
// on parsed AST attribute nodes instead of cue.Value.
//
// When re is nil (HasAttr mode): the field is included only if it
// carries the @attrName attribute.
//
// When re is non-nil (FilterByAttr mode): the field is included if
// it has no @attrName attribute (unannotated = include-all default),
// or if any pipe-separated segment of the attribute value matches re.
func astAttrMatches(f *ast.Field, attrName string, re *regexp.Regexp) bool {
	target := "@" + attrName + "("
	var found *ast.Attribute
	for _, a := range f.Attrs {
		if strings.HasPrefix(a.Text, target) {
			found = a
			break
		}
	}

	if re == nil {
		return found != nil
	}
	if found == nil {
		return true
	}

	// Extract the content between @attrName( and the closing ).
	body := found.Text[len(target) : len(found.Text)-1]
	// Parse attribute body into arguments, respecting key=value pairs.
	for _, arg := range splitAttrArgs(body) {
		// For keyed args like actor="customer", extract the value part.
		actual := arg
		if idx := strings.Index(arg, "="); idx >= 0 {
			actual = arg[idx+1:]
		}
		// Strip surrounding quotes if present.
		actual = strings.Trim(actual, "\"")
		for _, seg := range strings.Split(actual, "|") {
			if re.MatchString(strings.TrimSpace(seg)) {
				return true
			}
		}
	}
	return false
}

// splitAttrArgs splits an attribute body by commas, respecting quoted strings.
func splitAttrArgs(body string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	for _, r := range body {
		switch {
		case r == '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case r == ',' && !inQuote:
			args = append(args, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, strings.TrimSpace(current.String()))
	}
	return args
}
