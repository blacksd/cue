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
	"cuelang.org/go/internal/core/export"
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

func hasAttr(ctx *adt.OpContext, s pkg.Schema, attrName string) (ast.Expr, error) {
	return filterVertexByAttr(ctx, s, attrName, nil)
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
// At the top level, fields with an attribute whose value does not match
// are excluded (even if descendants would match — the parent's explicit
// annotation takes precedence). Unannotated top-level fields are excluded
// unless they are structs that contain a descendant with an explicit
// matching attribute.
//
// Inside a matching struct, unannotated fields are included by default
// (inherited pass-through), while fields with a non-matching attribute
// are still excluded. The filter is applied recursively to nested structs.
//
// Optional fields remain optional in the output.
func FilterByAttr(s pkg.Schema, attrName, pattern string) (ast.Expr, error) {
	ctx := value.OpContext(s)
	return filterByAttr(ctx, s, attrName, pattern)
}

func filterByAttr(ctx *adt.OpContext, s pkg.Schema, attrName, pattern string) (ast.Expr, error) {
	re, err := compilePattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("FilterByAttr: invalid pattern: %w", err)
	}
	return filterVertexByAttr(ctx, s, attrName, re)
}

// compilePattern compiles a regex pattern, auto-anchoring it if the
// user has not provided explicit ^ or $ anchors.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(pattern, "^") && !strings.HasSuffix(pattern, "$") {
		pattern = "^" + pattern + "$"
	}
	return regexp.Compile(pattern)
}

// filterVertexByAttr builds a filtered vertex containing only matching arcs,
// then exports it through the standard export pipeline with InlineImports
// to produce a self-contained AST expression. This approach correctly handles
// cross-package references because the export pivotter only discovers
// references for surviving fields — no orphaned let clauses.
func filterVertexByAttr(ctx *adt.OpContext, v cue.Value, attrName string, re *regexp.Regexp) (ast.Expr, error) {
	r, src := value.ToInternal(v)
	filtered := buildFilteredVertex(ctx, src, v, attrName, re, 0)

	p := export.Profile{
		Simplify:      true,
		ShowOptional:  true,
		InlineImports: true,
	}

	f, exportErr := p.Vertex(r, "_", filtered)
	if exportErr != nil {
		return nil, fmt.Errorf("FilterByAttr: export error: %v", exportErr)
	}

	return fileToExpr(f), nil
}

// buildFilteredVertex creates a new vertex containing only arcs whose fields
// match the attribute filter. For struct-typed arcs, it recurses to filter
// nested fields as well.
func buildFilteredVertex(ctx *adt.OpContext, src *adt.Vertex, v cue.Value, attrName string, re *regexp.Regexp, depth int) *adt.Vertex {
	filtered := src.Clone()
	filtered.Arcs = nil
	filtered.Structs = nil
	filtered.BaseValue = &adt.StructMarker{}

	sl := &adt.StructLit{}

	iter, _ := v.Fields(cue.Optional(true))
	for iter.Next() {
		sel := iter.Selector()
		if !sel.IsString() {
			continue
		}
		fieldVal := iter.Value()

		matched := attrMatches(fieldVal, attrName, re, depth)
		if !matched && depth == 0 && fieldVal.IncompleteKind() == cue.StructKind {
			// Unannotated struct at root: include if any descendant matches.
			// Only for truly unannotated fields — if the field carries the
			// attribute but doesn't match, respect the explicit annotation.
			a := fieldVal.Attribute(attrName)
			if a.Err() != nil {
				matched = hasMatchingDescendant(fieldVal, attrName, re)
			}
		}
		if !matched {
			continue
		}

		label := ctx.StringLabel(sel.Unquoted())
		arc := src.LookupRaw(label)
		if arc == nil {
			continue
		}

		if fieldVal.IncompleteKind() == cue.StructKind {
			filteredArc := buildFilteredVertex(ctx, arc.DerefValue(), fieldVal, attrName, re, depth+1)
			filteredArc.Label = arc.Label
			filteredArc.ArcType = arc.ArcType
			filtered.Arcs = append(filtered.Arcs, filteredArc)
		} else {
			filtered.Arcs = append(filtered.Arcs, arc)
		}

		sl.Decls = append(sl.Decls, &adt.Field{Label: label, Value: &adt.Top{}})
	}

	filtered.AddStruct(sl)
	return filtered
}

// hasMatchingDescendant reports whether any field nested inside v
// explicitly carries a @attrName attribute whose value matches re.
// Unannotated fields are not counted — only explicit attribute matches
// qualify, so that an unannotated parent struct is included only when
// a descendant is genuinely tagged for the target view.
func hasMatchingDescendant(v cue.Value, attrName string, re *regexp.Regexp) bool {
	iter, _ := v.Fields(cue.Optional(true))
	for iter.Next() {
		fv := iter.Value()
		if hasExplicitMatch(fv, attrName, re) {
			return true
		}
		if fv.IncompleteKind() == cue.StructKind && hasMatchingDescendant(fv, attrName, re) {
			return true
		}
	}
	return false
}

// hasExplicitMatch reports whether the field carries @attrName and
// a pipe-separated segment of the attribute value matches re.
func hasExplicitMatch(v cue.Value, attrName string, re *regexp.Regexp) bool {
	attr := v.Attribute(attrName)
	if attr.Err() != nil {
		return false
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

// fileToExpr extracts a single expression from an exported ast.File,
// skipping package and import declarations. If the file contains only
// a single embed (the common case), returns its expression directly.
// Otherwise wraps all remaining declarations in a StructLit to
// preserve any let clauses alongside the struct fields.
func fileToExpr(f *ast.File) ast.Expr {
	var decls []ast.Decl
	for _, d := range f.Decls {
		switch d.(type) {
		case *ast.Package, *ast.ImportDecl:
			continue
		default:
			decls = append(decls, d)
		}
	}
	if len(decls) == 1 {
		if e, ok := decls[0].(*ast.EmbedDecl); ok {
			return e.Expr
		}
	}
	return &ast.StructLit{Elts: decls}
}

// TransformKeys returns a copy of s with all field names transformed by
// the named transformation function. It operates recursively on nested
// structs. Optional fields remain optional. Attributes are stripped from
// the output (consistent with FilterByAttr behavior).
//
// When a transform returns an empty string, the field is dropped from the
// output (same semantics as FilterByAttr excluding a field).
//
// Supported transforms:
//   - "kebabToCamel": converts kebab-case field names to camelCase.
//   - "sanitizeKoala": strips koala XML encoding artifacts from field names.
//     Drops text content markers ($$) and XML namespace declarations
//     ($xmlns:*, $xsi:*). Strips the $ prefix from remaining $-prefixed
//     fields, preserving any hyphens for later kebabToCamel conversion.
//
// Returns an error for unrecognized transform names.
func TransformKeys(s pkg.Schema, transformName string) (ast.Expr, error) {
	ctx := value.OpContext(s)
	return transformKeys(ctx, s, transformName)
}

func transformKeys(ctx *adt.OpContext, s pkg.Schema, transformName string) (ast.Expr, error) {
	fn, err := resolveTransform(transformName)
	if err != nil {
		return nil, err
	}

	r, src := value.ToInternal(s)
	transformed := buildTransformedVertex(ctx, src, s, fn)

	p := export.Profile{
		Simplify:      true,
		ShowOptional:  true,
		InlineImports: true,
	}

	f, exportErr := p.Vertex(r, "_", transformed)
	if exportErr != nil {
		return nil, fmt.Errorf("TransformKeys: export error: %v", exportErr)
	}

	return fileToExpr(f), nil
}

// buildTransformedVertex creates a new vertex with all arc labels
// transformed by fn. For struct-typed arcs, it recurses to transform
// nested field names as well.
func buildTransformedVertex(ctx *adt.OpContext, src *adt.Vertex, v cue.Value, fn func(string) string) *adt.Vertex {
	transformed := src.Clone()
	transformed.Arcs = nil
	transformed.Structs = nil
	transformed.BaseValue = &adt.StructMarker{}

	sl := &adt.StructLit{}

	iter, _ := v.Fields(cue.Optional(true))
	for iter.Next() {
		sel := iter.Selector()
		if !sel.IsString() {
			continue
		}
		fieldVal := iter.Value()

		oldName := sel.Unquoted()
		newName := fn(oldName)
		if newName == "" {
			continue
		}
		newLabel := ctx.StringLabel(newName)

		oldLabel := ctx.StringLabel(oldName)
		arc := src.LookupRaw(oldLabel)
		if arc == nil {
			continue
		}

		if fieldVal.IncompleteKind() == cue.StructKind {
			transformedArc := buildTransformedVertex(ctx, arc.DerefValue(), fieldVal, fn)
			transformedArc.Label = newLabel
			transformedArc.ArcType = arc.ArcType
			transformed.Arcs = append(transformed.Arcs, transformedArc)
		} else {
			cloned := *arc
			cloned.Label = newLabel
			transformed.Arcs = append(transformed.Arcs, &cloned)
		}

		sl.Decls = append(sl.Decls, &adt.Field{Label: newLabel, Value: &adt.Top{}})
	}

	transformed.AddStruct(sl)
	return transformed
}

// resolveTransform returns the transform function for the given name,
// or an error if the name is not recognized.
func resolveTransform(name string) (func(string) string, error) {
	switch name {
	case "kebabToCamel":
		return kebabToCamel, nil
	case "sanitizeKoala":
		return sanitizeKoala, nil
	default:
		return nil, fmt.Errorf("TransformKeys: unknown transform %q", name)
	}
}

// kebabToCamel converts kebab-case to camelCase.
func kebabToCamel(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// sanitizeKoala strips koala XML encoding artifacts from field names.
// Returns "" (drop) for text content markers ($$) and XML namespace
// declarations ($xmlns:*, $xsi:*). Strips the $ prefix from all other
// $-prefixed fields, including $+kebab hybrids like
// "$clear-client-password-on-connection".
func sanitizeKoala(s string) string {
	if s == "$$" {
		return ""
	}
	if strings.HasPrefix(s, "$xmlns:") || strings.HasPrefix(s, "$xsi:") {
		return ""
	}
	return strings.TrimPrefix(s, "$")
}

// attrMatches reports whether the field should be included based on
// the cue.Value's attributes.
//
// When re is nil (HasAttr mode): the field is included only if it
// carries the @attrName attribute.
//
// When re is non-nil (FilterByAttr mode): if the field has no
// @attrName attribute, it is excluded at depth 0 (root level; the
// caller handles struct-with-matching-descendants separately) and
// included at depth > 0 (nested inside a matching parent). If the
// attribute is present, the field is included when any pipe-separated
// segment of the attribute value matches re.
func attrMatches(v cue.Value, attrName string, re *regexp.Regexp, depth int) bool {
	attr := v.Attribute(attrName)
	hasAttr := attr.Err() == nil

	if re == nil {
		return hasAttr
	}
	if !hasAttr {
		return depth > 0
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
