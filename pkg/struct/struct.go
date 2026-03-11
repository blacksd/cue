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
		var val ast.Expr
		if fieldVal.IncompleteKind() == cue.StructKind {
			filtered, err := filterStruct(fieldVal, attrName, re)
			if err != nil {
				return nil, err
			}
			val = filtered
		} else {
			val = fieldVal.Syntax(cue.Raw()).(ast.Expr)
		}

		f := &ast.Field{Label: label, Value: val}
		if iter.IsOptional() {
			f.Constraint = token.OPTION
		}
		result.Elts = append(result.Elts, f)
	}
	return result, nil
}

// attrMatches reports whether the field should be included.
//
// When re is nil (HasAttr mode): the field is included only if it
// carries the @attrName attribute.
//
// When re is non-nil (FilterByAttr mode): the field is included if
// it has no @attrName attribute (unannotated = include-all default),
// or if any pipe-separated segment of the attribute value matches re.
// Only values are checked, never keys — for keyed args like
// actor="product|customer" only "product|customer" is inspected.
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
		// Attr.Arg returns (value, "") for positional args (API quirk)
		// and (key, value) for keyed args. Extract the actual value.
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
