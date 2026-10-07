package glob

import "strings"

// Confidence qualifies how much a collision finding can be trusted.
type Confidence string

const (
	// ConfidenceExact means rulefit constructed a concrete path that both
	// patterns match and that the allowlist accepts.
	ConfidenceExact Confidence = "exact"
	// ConfidenceUnknown means the two patterns provably overlap, but no concrete
	// path could be constructed — typically because one side is anchored on a
	// whole file name ("**/pom.xml") while the other pins an extension. The
	// finding is still worth reporting, but it must be reported as unproven.
	ConfidenceUnknown Confidence = "unknown"
)

// Kind classifies an overlap. It is what turns "these two patterns both match
// something" into an actionable statement about rule selection, which for a
// first-match-wins map is decided by the declaration order.
type Kind string

const (
	// KindAShadowsB means every admissible path A matches is also matched by B.
	// A is declared first, so B's rule can never be selected.
	KindAShadowsB Kind = "a-shadows-b"
	// KindBShadowsA means B claims every admissible path A claims and more.
	// A still wins on its own (narrower) paths.
	KindBShadowsA Kind = "b-shadows-a"
	// KindPartial means the two patterns share some admissible paths but neither
	// contains the other, so which rule wins depends on the specific path.
	KindPartial Kind = "partial"
)

// Witness describes the overlap between an earlier pattern and a later one.
type Witness struct {
	// Path is a concrete path both patterns match, empty when the overlap was
	// proven structurally but no path could be constructed.
	Path string
	// Confidence qualifies Path.
	Confidence Confidence
	// Kind says which side, if any, is completely shadowed.
	Kind Kind
	// Verified counts how many independently constructed paths were checked
	// against both patterns, which is what makes Confidence and Kind assertions
	// rather than guesses.
	Verified int
}

// maxInstantiations bounds the number of concrete paths derived from one witness
// glob, so that a pathological pattern cannot blow up the search.
const maxInstantiations = 512

// FindOverlap reports how two patterns declared in order (a first, b second)
// interact.
//
// accept is the allowlist gate: a candidate path must satisfy it to count. A nil
// accept rejects every candidate, because "these two patterns overlap on some
// path" is not a finding unless the tool would actually process that path.
//
// Containment is measured, not asserted: the parser constructs admissible paths
// from each pattern and checks them against the other with the matcher, so Kind
// is only ever reported after concrete verification.
func FindOverlap(a, b Pattern, accept func(path string) bool) *Witness {
	aPaths := verifiedPaths(a, accept)
	bPaths := verifiedPaths(b, accept)
	shared := countShared(a, b, aPaths, accept)

	aContained := len(aPaths) > 0 && countMatching(b, aPaths) == len(aPaths)
	bContained := len(bPaths) > 0 && countMatching(a, bPaths) == len(bPaths)

	switch {
	case aContained && bContained:
		// Identical reach; A is declared first, so A's rule always wins.
		return &Witness{Path: firstShared(a, b, aPaths, accept), Confidence: ConfidenceExact, Kind: KindAShadowsB, Verified: shared}
	case aContained:
		return &Witness{Path: firstShared(a, b, aPaths, accept), Confidence: ConfidenceExact, Kind: KindAShadowsB, Verified: shared}
	case bContained:
		return &Witness{Path: firstShared(a, b, aPaths, accept), Confidence: ConfidenceExact, Kind: KindBShadowsA, Verified: shared}
	case shared > 0:
		return &Witness{Path: firstShared(a, b, aPaths, accept), Confidence: ConfidenceExact, Kind: KindPartial, Verified: shared}
	}

	// No admissible path was constructed. Report a structural overlap only when
	// the two patterns really do share a path shape; two patterns pinning
	// different extensions are disjoint and must stay silent.
	if structuralOverlap(a, b) {
		return &Witness{Confidence: ConfidenceUnknown, Kind: KindPartial}
	}
	return nil
}

// verifiedPaths returns concrete admissible paths the pattern matches.
func verifiedPaths(p Pattern, accept func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, alt := range p.Alternates {
		if len(alt.Segments) == 0 {
			continue
		}
		frag, ok := remainingSegments(alt.Segments)
		if !ok {
			continue
		}
		for _, candidate := range instantiate(frag) {
			if seen[candidate] || !p.Match(candidate) {
				continue
			}
			if accept == nil || !accept(candidate) {
				continue
			}
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}

func countMatching(p Pattern, paths []string) int {
	n := 0
	for _, path := range paths {
		if p.Match(path) {
			n++
		}
	}
	return n
}

// countShared counts constructed paths that both patterns match and the
// allowlist accepts.
func countShared(a, b Pattern, candidates []string, accept func(string) bool) int {
	n := 0
	for _, candidate := range candidates {
		if !a.Match(candidate) || !b.Match(candidate) {
			continue
		}
		if accept == nil || !accept(candidate) {
			continue
		}
		n++
	}
	return n
}

// firstShared returns the most readable shared path, preferring one that this
// function itself verified over one derived from the structural intersection.
func firstShared(a, b Pattern, candidates []string, accept func(string) bool) string {
	best := ""
	consider := func(candidate string) {
		if best == "" || preferred(candidate, best) {
			best = candidate
		}
	}
	for _, candidate := range candidates {
		if !a.Match(candidate) || !b.Match(candidate) {
			continue
		}
		if accept == nil || !accept(candidate) {
			continue
		}
		consider(candidate)
	}
	if best != "" {
		return best
	}
	for _, left := range a.Alternates {
		for _, right := range b.Alternates {
			if !segmentsOverlap(left.Segments, right.Segments) {
				continue
			}
			for _, segs := range [][]Segment{left.Segments, right.Segments} {
				frag, ok := remainingSegments(segs)
				if !ok {
					continue
				}
				for _, candidate := range instantiate(frag) {
					if !a.Match(candidate) || !b.Match(candidate) {
						continue
					}
					if accept == nil || !accept(candidate) {
						continue
					}
					consider(candidate)
				}
			}
		}
	}
	return best
}

// preferred keeps the reported path stable and readable: shorter first, then
// lexicographic, so output does not depend on iteration order.
func preferred(candidate, current string) bool {
	if len(candidate) != len(current) {
		return len(candidate) < len(current)
	}
	return candidate < current
}

// structuralOverlap reports whether two patterns can match a common path, used
// when no concrete admissible path could be constructed. It is deliberately
// conservative: an unprovable relationship is reported rather than hidden, so
// the only patterns it silences are ones it can prove disjoint.
func structuralOverlap(a, b Pattern) bool {
	for _, left := range a.Alternates {
		for _, right := range b.Alternates {
			if segmentsOverlap(left.Segments, right.Segments) {
				return true
			}
		}
	}
	return false
}

// segmentsOverlap decides whether two segment lists can match a common path.
//
// It is a product construction over the two NFA-ish globs. `**` is spontaneous
// — it can match zero segments — while every other segment consumes text and
// must therefore be reconciled with the other side, either by matching it
// character by character or by being absorbed whole by the other side's star.
//
// The construction is conservative in one direction on purpose: it reports
// disjointness only when reconciliation demonstrably fails. A path rule pair it
// cannot refute is reported as overlapping, which surfaces as an "unproven"
// finding rather than a silent miss.
func segmentsOverlap(a, b []Segment) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	states := map[[2]int]bool{}
	visited := map[[2]int]bool{}
	return convergence(a, b, 0, 0, states, visited)
}

// convergence reports whether the remaining segment lists can be reconciled.
//
// A leading `**` on either side may stand for nothing, which lets the two sides
// realign, so this walks the spontaneous transitions to a fixpoint.
func convergence(a, b []Segment, ai, bi int, states, visited map[[2]int]bool) bool {
	key := [2]int{ai, bi}
	if states[key] {
		return true
	}
	if visited[key] {
		return false
	}
	visited[key] = true

	switch {
	case ai == len(a) && bi == len(b):
		states[key] = true
		return true

	case ai == len(a):
		// Only b remains; each of its remaining segments must be `**`, since a
		// concrete segment needs a counterpart to be reconciled with.
		if convergenceTail(b[bi:]) {
			states[key] = true
			return true
		}
		return false

	case bi == len(b):
		if convergenceTail(a[ai:]) {
			states[key] = true
			return true
		}
		return false
	}

	if a[ai].DoubleStar {
		if convergence(a, b, ai+1, bi, states, visited) {
			states[key] = true
			return true
		}
		// The `**` spans this segment and keeps spanning.
		if convergence(a, b, ai, bi+1, states, visited) {
			states[key] = true
			return true
		}
		return false
	}
	if b[bi].DoubleStar {
		if convergence(a, b, ai, bi+1, states, visited) {
			states[key] = true
			return true
		}
		if convergence(a, b, ai+1, bi, states, visited) {
			states[key] = true
			return true
		}
		return false
	}

	if segmentAbsorbedBy(a[ai], b[bi]) || segmentAbsorbedBy(b[bi], a[ai]) || segmentsIntersect(a[ai], b[bi]) {
		if convergence(a, b, ai+1, bi+1, states, visited) {
			states[key] = true
			return true
		}
	}
	return false
}

func convergenceTail(segs []Segment) bool {
	for _, s := range segs {
		if !s.DoubleStar {
			return false
		}
	}
	return true
}

// segmentAbsorbedBy reports whether a star in x can stand for every string y
// matches. `**/*.xml` is absorbed by `**/*`, for example.
func segmentAbsorbedBy(y, x Segment) bool {
	var hasStar bool
	for _, e := range x.Elems {
		if e.Kind == KindStar {
			hasStar = true
			break
		}
	}
	if !hasStar {
		return false
	}
	switch len(x.Elems) {
	case 1:
		return true
	case 2:
		return x.Elems[0].Kind == KindStar && x.Elems[1].Kind == KindStar
	}
	// More complex star patterns are not proven absorbing here; the
	// character-level check below still runs.
	return false
}

// segmentsIntersect reports whether two concrete segments can match a common
// string, by reconciling their elements one character at a time. The star
// element is the only one that can consume a variable amount of text, so it is
// the recursion's branching point.
func segmentsIntersect(x, y Segment) bool {
	memo := map[[2]int]bool{}
	return elementsIntersect(x.Elems, y.Elems, 0, 0, memo)
}

func elementsIntersect(x, y []Elem, i, j int, memo map[[2]int]bool) bool {
	key := [2]int{i, j}
	if v, ok := memo[key]; ok {
		return v
	}
	memo[key] = false

	var result bool
	switch {
	case i == len(x) && j == len(y):
		result = true

	case i < len(x) && x[i].Kind == KindStar:
		result = elementsIntersect(x, y, i+1, j, memo) ||
			(j < len(y) && elementsIntersect(x, y, i, j+1, memo))

	case j < len(y) && y[j].Kind == KindStar:
		result = elementsIntersect(x, y, i, j+1, memo) ||
			(i < len(x) && elementsIntersect(x, y, i+1, j, memo))

	case i < len(x) && j < len(y):
		result = elementsCompatible(x[i], y[j]) && elementsIntersect(x, y, i+1, j+1, memo)
	}
	memo[key] = result
	return result
}

// elementsCompatible reports whether two single-character elements can match the
// same byte.
func elementsCompatible(a, b Elem) bool {
	if a.Kind == KindAny || b.Kind == KindAny {
		return true
	}
	if a.Kind == KindLiteral {
		if a.Literal == "" {
			return false
		}
		return b.matchesByte(a.Literal[0])
	}
	if b.Kind == KindLiteral {
		if b.Literal == "" {
			return false
		}
		return a.matchesByte(b.Literal[0])
	}
	if a.Kind == KindClass && b.Kind == KindClass {
		for _, ra := range a.Class {
			for c := int(ra.Lo); c <= int(ra.Hi); c++ {
				if b.matchesByte(byte(c)) {
					return true
				}
			}
		}
	}
	return false
}

// remainingSegments renders a segment list as its own witness glob.
func remainingSegments(segs []Segment) (string, bool) {
	if len(segs) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s.DoubleStar {
			parts = append(parts, "**")
			continue
		}
		fragment, ok := SelfSegmentGlob(s)
		if !ok {
			return "", false
		}
		parts = append(parts, fragment)
	}
	return strings.Join(parts, "/"), true
}

// SegmentsOverlap reports whether two whole patterns can match a common path.
// It is the exported form of the check the collision detector uses.
func SegmentsOverlap(a, b Pattern) bool { return structuralOverlap(a, b) }

// SelfSegmentGlob renders one segment as a glob matching the same strings.
func SelfSegmentGlob(s Segment) (string, bool) {
	var b strings.Builder
	for _, e := range s.Elems {
		switch e.Kind {
		case KindStar:
			b.WriteString("*")
		case KindAny:
			b.WriteString("?")
		case KindLiteral:
			b.WriteString(escapeLiteral(e.Literal))
		case KindClass:
			frag := renderClass(e)
			if frag == "" {
				return "", false
			}
			b.WriteString(frag)
		}
	}
	return b.String(), true
}

// pinnedElement returns the single character an element pins down, if it pins
// one. A class is not treated as pinned: it stands for a set.
func pinnedElement(e Elem) (string, bool) {
	if e.Kind == KindLiteral && e.Literal != "" {
		return escapeLiteral(e.Literal), true
	}
	return "", false
}

func intersectElements(x, y Elem) (string, bool) {
	switch {
	case x.Kind == KindAny && y.Kind == KindAny:
		return "?", true
	case x.Kind == KindAny:
		return selfElementGlob(y)
	case y.Kind == KindAny:
		return selfElementGlob(x)
	case x.Kind == KindLiteral && y.Kind == KindLiteral:
		if x.Literal == y.Literal {
			return escapeLiteral(x.Literal), true
		}
		return "", false
	case x.Kind == KindLiteral:
		if y.Kind == KindClass {
			if x.Literal == "" || !y.matchesByte(x.Literal[0]) {
				return "", false
			}
			return escapeLiteral(x.Literal), true
		}
		return "", false
	case y.Kind == KindLiteral:
		if x.Kind == KindClass {
			if y.Literal == "" || !x.matchesByte(y.Literal[0]) {
				return "", false
			}
			return escapeLiteral(y.Literal), true
		}
		return "", false
	case x.Kind == KindClass && y.Kind == KindClass:
		class, ok := intersectClasses(x, y)
		if !ok {
			return "", false
		}
		return renderClass(Elem{Kind: KindClass, Class: class}), true
	}
	return "", false
}

func selfElementGlob(e Elem) (string, bool) {
	switch e.Kind {
	case KindAny:
		return "?", true
	case KindLiteral:
		return escapeLiteral(e.Literal), true
	case KindClass:
		frag := renderClass(e)
		return frag, frag != ""
	}
	return "", false
}

func intersectClasses(x, y Elem) ([]ClassRange, bool) {
	var out []ClassRange
	if !x.Negate && !y.Negate {
		for _, rx := range x.Class {
			for c := int(rx.Lo); c <= int(rx.Hi); c++ {
				if y.matchesByte(byte(c)) {
					out = append(out, ClassRange{Lo: byte(c), Hi: byte(c)})
				}
			}
		}
	} else {
		// Negated classes are rare in path rules; enumerate the printable range.
		for c := 32; c < 127; c++ {
			if x.matchesByte(byte(c)) && y.matchesByte(byte(c)) {
				out = append(out, ClassRange{Lo: byte(c), Hi: byte(c)})
			}
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func renderClass(e Elem) string {
	if len(e.Class) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	if e.Negate {
		b.WriteByte('!')
	}
	for _, r := range e.Class {
		switch {
		case r.Lo == r.Hi:
			if r.Lo == ']' || r.Lo == '[' || r.Lo == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(r.Lo)
		default:
			b.WriteByte(r.Lo)
			b.WriteByte('-')
			b.WriteByte(r.Hi)
		}
	}
	b.WriteByte(']')
	return b.String()
}

func escapeLiteral(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// instantiate turns a witness glob into concrete paths by replacing every `*`
// with "" or "x" and every `?` with "a". Empty replacements matter: they are
// what lets `*mapper*.xml` be witnessed by `mapper.xml`. A `**` yields both the
// top-level form and a one-directory-deep form, so patterns keep working at the
// root of a repository.
func instantiate(witnessGlob string) []string {
	if witnessGlob == "" {
		return nil
	}
	out := make([]string, 0, 8)
	walkInstantiations(strings.Split(witnessGlob, "/"), 0, "", &out)
	return out
}

func walkInstantiations(segments []string, idx int, prefix string, out *[]string) {
	if len(*out) >= maxInstantiations {
		return
	}
	if idx == len(segments) {
		if prefix != "" {
			*out = append(*out, strings.TrimSuffix(prefix, "/"))
		}
		return
	}
	if segments[idx] == "**" {
		walkInstantiations(segments, idx+1, prefix, out)
		walkInstantiations(segments, idx+1, prefix+"dir/", out)
		return
	}
	for _, variant := range instantiateSegment(segments[idx]) {
		walkInstantiations(segments, idx+1, prefix+variant+"/", out)
		if len(*out) >= maxInstantiations {
			return
		}
	}
}

// instantiateSegment expands one segment's `*` and `?` elements into concrete
// variants. `**` is left alone: the caller substitutes it.
func instantiateSegment(segment string) []string {
	if segment == "**" {
		return []string{"**"}
	}
	variants := []string{""}
	for i := 0; i < len(segment); {
		switch segment[i] {
		case '\\':
			if i+1 < len(segment) {
				variants = appendChar(variants, string(segment[i+1]))
				i += 2
				continue
			}
			variants = appendChar(variants, "\\")
			i++
		case '*':
			next := make([]string, 0, len(variants)*2)
			for _, v := range variants {
				next = append(next, v, v+"x")
			}
			variants = dedupeStrings(next)
			i++
		case '?':
			variants = appendChar(variants, "a")
			i++
		case '[':
			class, _, next, ok := parseClass(segment, i)
			if !ok {
				variants = appendChar(variants, "[")
				i++
				continue
			}
			chars := classChars(class)
			if len(chars) == 0 {
				return nil
			}
			expanded := make([]string, 0, len(variants)*len(chars))
			for _, v := range variants {
				for _, c := range chars {
					expanded = append(expanded, v+c)
				}
			}
			variants = dedupeStrings(expanded)
			i = next
		default:
			variants = appendChar(variants, string(segment[i]))
			i++
		}
		if len(variants) > maxInstantiations {
			return variants[:maxInstantiations]
		}
	}
	return variants
}

func appendChar(variants []string, c string) []string {
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		out = append(out, v+c)
	}
	return out
}

func classChars(class []ClassRange) []string {
	var out []string
	for _, r := range class {
		for c := int(r.Lo); c <= int(r.Hi) && len(out) < 8; c++ {
			out = append(out, string(byte(c)))
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
