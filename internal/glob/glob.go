// Package glob implements the small, well-defined subset of glob semantics that
// rulefit needs, and — unlike a plain matcher — it can intersect two patterns and
// produce a concrete path that both of them match.
//
// # Dialect
//
// Within one path segment (`/` always separates segments):
//
//	*        any run of characters, including none
//	?        exactly one character
//	[a-z]    one character from the set; a leading ! or ^ negates the set
//	\x       the literal character x
//	{...}    alternation, may nest, always expands before matching
//
// The segment `**` on its own matches zero or more whole segments. Every other
// segment matches exactly one segment.
//
// Matching is byte-oriented and case-sensitive.
//
// This dialect is intentionally narrower than filepath.Match and than
// github.com/bmatcuk/doublestar. The differences that matter in practice are
// documented in the repository README under "Scope and non-goals".
package glob

import (
	"fmt"
	"strings"
)

// maxAlternates bounds brace expansion so that a hostile or accidental config
// cannot explode into millions of patterns.
const maxAlternates = 512

// Pattern is a compiled glob.
type Pattern struct {
	Raw        string
	Alternates []Alternate
	// Warning is non-empty when the source pattern is not in the dialect, for
	// example because a brace is unbalanced. The pattern is still usable: the
	// offending construct is treated as a literal.
	Warning string
}

// Alternate is one brace-expanded alternative of a Pattern.
type Alternate struct {
	Segments []Segment
}

// Segment is one `/`-separated piece of a path.
type Segment struct {
	// DoubleStar reports the `**` segment, which spans zero or more segments.
	DoubleStar bool
	Elems      []Elem
}

// Elem is one matching element inside a segment.
type Elem struct {
	Kind    ElemKind
	Literal string // KindLiteral, KindStar, KindAny
	Class   []ClassRange
	Negate  bool
}

// ElemKind distinguishes the element forms.
type ElemKind int

const (
	// KindLiteral matches a fixed string.
	KindLiteral ElemKind = iota
	// KindStar matches any run of characters, including none.
	KindStar
	// KindAny matches exactly one character.
	KindAny
	// KindClass matches one character from Class.
	KindClass
)

// ClassRange is one character or inclusive range inside `[...]`.
type ClassRange struct {
	Lo, Hi byte
}

// Parse compiles a pattern. It never fails: constructs outside the dialect are
// treated as literals and reported through Pattern.Warning so that a caller can
// surface them instead of silently mis-certifying a config.
func Parse(raw string) Pattern {
	alternates, status := expandBraces(raw)
	if len(alternates) == 0 {
		alternates = []string{raw}
	}
	if len(alternates) > maxAlternates {
		alternates = alternates[:maxAlternates]
		status = bracesCapped
	}
	p := Pattern{Raw: raw}
	switch status {
	case bracesUnbalanced:
		p.Warning = "unbalanced braces treated as literals"
	case bracesCapped:
		p.Warning = fmt.Sprintf("brace expansion exceeded %d alternatives; extras ignored", maxAlternates)
	}
	for _, alt := range alternates {
		p.Alternates = append(p.Alternates, Alternate{Segments: splitSegments(alt)})
	}
	return p
}

// Match reports whether path matches any alternate of the pattern.
func (p Pattern) Match(path string) bool {
	parts := strings.Split(path, "/")
	for _, alt := range p.Alternates {
		if matchSegments(alt.Segments, parts, 0, 0, map[[2]int]bool{}) {
			return true
		}
	}
	return false
}

// Match uses the given compiled pattern to test path.
func Match(p Pattern, path string) bool { return p.Match(path) }

func splitSegments(pattern string) []Segment {
	raw := strings.Split(pattern, "/")
	segs := make([]Segment, 0, len(raw))
	for _, s := range raw {
		if s == "**" {
			segs = append(segs, Segment{DoubleStar: true})
			continue
		}
		segs = append(segs, Segment{Elems: parseElems(s)})
	}
	return segs
}

func parseElems(seg string) []Elem {
	elems := make([]Elem, 0, len(seg))
	for i := 0; i < len(seg); {
		switch c := seg[i]; c {
		case '*':
			if n := len(elems); n > 0 && elems[n-1].Kind == KindStar {
				i++ // collapse "**" inside a segment into one star
				continue
			}
			elems = append(elems, Elem{Kind: KindStar})
			i++
		case '?':
			elems = append(elems, Elem{Kind: KindAny})
			i++
		case '[':
			class, negate, next, ok := parseClass(seg, i)
			if !ok {
				elems = append(elems, literal(string(c)))
				i++
				continue
			}
			elems = append(elems, Elem{Kind: KindClass, Class: class, Negate: negate})
			i = next
		case '\\':
			if i+1 < len(seg) {
				elems = append(elems, literal(string(seg[i+1])))
				i += 2
				continue
			}
			elems = append(elems, literal("\\"))
			i++
		default:
			elems = append(elems, literal(string(c)))
			i++
		}
	}
	return elems
}

func literal(s string) Elem { return Elem{Kind: KindLiteral, Literal: s} }

// parseClass parses "[...]" starting at seg[start]. It returns ok=false when the
// class is unterminated, so the caller can fall back to a literal '['.
func parseClass(seg string, start int) (class []ClassRange, negate bool, next int, ok bool) {
	i := start + 1
	if i < len(seg) && (seg[i] == '!' || seg[i] == '^') {
		negate = true
		i++
	}
	// A ']' directly after the (possibly negated) opening bracket is a literal.
	first := true
	for i < len(seg) {
		if seg[i] == ']' && !first {
			return class, negate, i + 1, true
		}
		first = false
		lo := seg[i]
		if lo == '\\' && i+1 < len(seg) {
			i++
			lo = seg[i]
		}
		if i+2 < len(seg) && seg[i+1] == '-' && seg[i+2] != ']' {
			hi := seg[i+2]
			if hi == '\\' && i+3 < len(seg) {
				hi = seg[i+3]
				i++
			}
			class = append(class, ClassRange{Lo: lo, Hi: hi})
			i += 3
			continue
		}
		class = append(class, ClassRange{Lo: lo, Hi: lo})
		i++
	}
	return nil, false, 0, false
}

func (e Elem) matchesByte(b byte) bool {
	switch e.Kind {
	case KindStar, KindAny:
		return true
	case KindLiteral:
		return e.Literal == string(b)
	case KindClass:
		in := false
		for _, r := range e.Class {
			if b >= r.Lo && b <= r.Hi {
				in = true
				break
			}
		}
		return in != e.Negate
	}
	return false
}

func matchSegments(pattern []Segment, path []string, pi, si int, memo map[[2]int]bool) bool {
	key := [2]int{pi, si}
	if v, ok := memo[key]; ok {
		return v
	}
	// Guard against recursion through "**" revisiting the same state.
	memo[key] = false

	var result bool
	switch {
	case pi == len(pattern):
		result = si == len(path)
	case pattern[pi].DoubleStar:
		// "**" matches zero segments, or consumes the next segment and retries.
		result = matchSegments(pattern, path, pi+1, si, memo)
		if !result && si < len(path) {
			result = matchSegments(pattern, path, pi, si+1, memo)
		}
	default:
		result = si < len(path) && matchSegment(pattern[pi], path[si], 0, 0, map[[2]int]bool{}) &&
			matchSegments(pattern, path, pi+1, si+1, memo)
	}
	memo[key] = result
	return result
}

func matchSegment(seg Segment, s string, ei, ci int, memo map[[2]int]bool) bool {
	// A `*` element can consume any number of characters, so the pair (ei, ci)
	// does not identify its state uniquely: two different consumptions of the
	// same star reach the same key from different positions. Only concrete
	// elements are memoised, which keeps the star branches independent.
	if ei < len(seg.Elems) && seg.Elems[ei].Kind == KindStar {
		if matchSegment(seg, s, ei+1, ci, memo) {
			return true
		}
		return ci < len(s) && matchSegment(seg, s, ei, ci+1, memo)
	}

	key := [2]int{ei, ci}
	if v, ok := memo[key]; ok {
		return v
	}

	var result bool
	switch {
	case ei == len(seg.Elems):
		result = ci == len(s)
	default:
		result = ci < len(s) && seg.Elems[ei].matchesByte(s[ci]) && matchSegment(seg, s, ei+1, ci+1, memo)
	}
	memo[key] = result
	return result
}

// braceStatus reports what brace expansion did, so that Parse can surface an
// unusable pattern instead of silently certifying it.
type braceStatus int

const (
	bracesExpanded braceStatus = iota // at least one group was expanded
	bracesNone                        // no braces anywhere
	bracesUnbalanced                  // an unpaired brace was found and kept literal
	bracesCapped                      // expansion hit maxAlternates
)

// expandBraces expands every top-level and nested `{a,b}` group. Unpaired braces
// are kept literal.
func expandBraces(pattern string) ([]string, braceStatus) {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		if strings.ContainsRune(pattern, '}') {
			return []string{pattern}, bracesUnbalanced
		}
		return []string{pattern}, bracesNone
	}
	depth := 0
	close := -1
	for i := open; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth == 0 {
			close = i
			break
		}
	}
	if close < 0 {
		return []string{pattern}, bracesUnbalanced
	}

	status := bracesExpanded
	var out []string
	for _, option := range splitTopLevel(pattern[open+1 : close]) {
		expanded, childStatus := expandBraces(pattern[:open] + option + pattern[close+1:])
		if childStatus > status {
			status = childStatus
		}
		out = append(out, expanded...)
		if len(out) > maxAlternates {
			return out[:maxAlternates], bracesCapped
		}
	}
	return out, status
}

// splitTopLevel splits on commas that are not nested inside braces.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
