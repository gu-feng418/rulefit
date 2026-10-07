package glob

import "testing"

func TestParseAndMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.properties", "src/main/resources/app.properties", true},
		{"**/*.properties", "app.properties", true},
		{"**/*.properties", "src/app.yaml", false},
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", false},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/a/b/main.go", false},
		{"src/**", "src/a/b/main.go", true},
		{"src/**/*.go", "src/main.go", true},
		{"src/**/*.go", "src/a/b/main.go", true},
		{"src/**/*.go", "src/a/b/main.ts", false},
		{"**/*.{json,json5}", "a/b/config.json5", true},
		{"**/*.{json,json5}", "a/b/config.yaml", false},
		{"**/*{mapper,dao}*.xml", "src/UserMapper.xml", false}, // lowercase literals do not match
		{"**/*{Mapper,Dao}*.xml", "src/UserMapper.xml", true},
		{"**/*{Mapper,Dao}*.xml", "src/UserDaoImpl.xml", true},
		{"**/*mapper*.xml", "src/usermapper.xml", true},
		{".github/workflows/**/*.{yaml,yml}", ".github/workflows/ci.yml", true},
		{".github/**/*.{yaml,yml}", ".github/workflows/ci.yml", true},
		{"**/pom.xml", "backend/pom.xml", true},
		{"**/pom.xml", "backend/pom.yaml", false},
		{"**/*.R", "analysis/plot.R", true},
		{"**/*.R", "analysis/plot.r", false}, // case-sensitive on purpose
		{"a/?/c", "a/b/c", true},
		{"a/?/c", "a/bb/c", false},
		{"**/*[0-9].go", "gen/x1.go", true},
		{"**/*[!0-9].go", "gen/xa.go", true},
		{"**/*[!0-9].go", "gen/x1.go", false},
		{"**", "anything/at/all.txt", true},
		{"**/*.tar.gz", "dist/pkg.tar.gz", true},
	}
	for _, tc := range tests {
		got := Parse(tc.pattern).Match(tc.path)
		if got != tc.want {
			t.Errorf("Parse(%q).Match(%q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestUnbalancedBraceWarnsAndIsNotSilentlyWrong(t *testing.T) {
	// `{` is treated as a literal, so the pattern really does not match a plain
	// `.json` file. The warning is what stops that from becoming a silent
	// mis-certification: the caller reports the pattern instead of trusting it.
	p := Parse("**/*.{json")
	if p.Warning == "" {
		t.Errorf("expected a warning for an unbalanced brace")
	}
	if p.Match("a/b/x.json") {
		t.Errorf("unbalanced brace should stay literal, so the pattern must not match a/b/x.json")
	}
	if !p.Match("a/b/x.{json") {
		t.Errorf("the literal brace must still be matchable")
	}

	q := Parse("**/*.json}")
	if q.Warning == "" {
		t.Errorf("expected a warning for an unmatched closing brace")
	}
	if !q.Match("a/b/x.json}") {
		t.Errorf("the literal closing brace must still be matchable")
	}
}

func TestNestedAndMultiBraceExpansion(t *testing.T) {
	p := Parse("**/*.{js,{ts,tsx}}")
	for _, path := range []string{"a/x.js", "a/x.ts", "a/x.tsx"} {
		if !p.Match(path) {
			t.Errorf("expected %s to match, alternates=%d", path, len(p.Alternates))
		}
	}
	if p.Match("a/x.jsx") {
		t.Errorf("unexpected match for a/x.jsx")
	}
}

func TestBraceExpansionIsBounded(t *testing.T) {
	p := Parse("{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}")
	if len(p.Alternates) != maxAlternates {
		t.Fatalf("alternates = %d, want exactly the cap %d", len(p.Alternates), maxAlternates)
	}
	if p.Warning == "" {
		t.Fatalf("expected a warning when expansion is capped")
	}
}

func TestSegmentsOverlapDisjoint(t *testing.T) {
	disjoint := [][2]string{
		{"**/*.properties", "**/*.xml"},
		{"**/*.txt", "**/*.md"},
		{"**/*.go", "**/*.rs"},
		{"**/pom.xml", "**/Cargo.toml"},
		{"src/*.go", "test/*.go"},
	}
	for _, pair := range disjoint {
		if SegmentsOverlap(Parse(pair[0]), Parse(pair[1])) {
			t.Errorf("%q and %q are disjoint but were reported as overlapping", pair[0], pair[1])
		}
	}

	overlapping := [][2]string{
		{"**/pom.xml", "**/*.xml"},
		{"**/*.xml", "**/*{Mapper,Dao}*.xml"},
		{".github/workflows/**/*.{yaml,yml}", ".github/**/*.{yaml,yml}"},
		{"**/*.{json,json5}", "**/*.json"},
		{"**/*.go", "**/*.go"},
		{"**/?", "**/*"},
	}
	for _, pair := range overlapping {
		if !SegmentsOverlap(Parse(pair[0]), Parse(pair[1])) {
			t.Errorf("%q and %q do overlap but were reported as disjoint", pair[0], pair[1])
		}
	}
}

// TestSegmentsOverlapAgreesWithMatching brute-forces short paths and checks that
// whenever one exists matching both patterns, SegmentsOverlap says so. A missed
// overlap would let a real shadowing bug go unreported, so this is the safety
// net for the structural check.
func TestSegmentsOverlapAgreesWithMatching(t *testing.T) {
	patterns := []string{
		"**/*.xml", "**/*.properties", "**/pom.xml", "*.go", "**/*.go",
		"**/*{Mapper,Dao}*.xml", ".github/**/*.{yaml,yml}",
		".github/workflows/**/*.{yaml,yml}", "**/*.{json,json5}", "**/*.json",
		"**/?", "**/*", "**/*[0-9].go",
	}
	alphabet := []byte("ab9.")
	paths := []string{}
	for _, a := range alphabet {
		paths = append(paths, string(a))
	}
	for _, a := range alphabet {
		for _, b := range alphabet {
			paths = append(paths, string([]byte{a, b}))
		}
	}
	extra := []string{"x.xml", "pom.xml", "a.go", "x9.go", "a.json", "dir/x.yml", ".github/ci.yaml", "dir/pom.xml"}
	paths = append(paths, extra...)

	for _, left := range patterns {
		for _, right := range patterns {
			shared := false
			for _, path := range paths {
				if Parse(left).Match(path) && Parse(right).Match(path) {
					shared = true
					break
				}
			}
			if shared && !SegmentsOverlap(Parse(left), Parse(right)) {
				t.Errorf("%q and %q share at least one of the probed paths but were reported disjoint", left, right)
			}
		}
	}
}

func TestFindOverlap(t *testing.T) {
	acceptYAML := func(p string) bool {
		return len(p) >= 5 && (p[len(p)-5:] == ".yaml" || p[len(p)-4:] == ".yml")
	}
	acceptJSON := func(p string) bool {
		return len(p) >= 5 && p[len(p)-5:] == ".json"
	}
	acceptXML := func(p string) bool {
		return len(p) >= 4 && p[len(p)-4:] == ".xml"
	}

	tests := []struct {
		name     string
		a, b     string
		accept   func(string) bool
		wantNil  bool
		wantPath string
		wantKind Kind
	}{
		{
			name:     "workflow rule is contained by the .github catch-all",
			a:        ".github/workflows/**/*.{yaml,yml}",
			b:        ".github/**/*.{yaml,yml}",
			accept:   acceptYAML,
			wantPath: ".github/workflows/.yml",
			wantKind: KindAShadowsB,
		},
		{
			name:    "different extensions never collide",
			a:       "**/*.properties",
			b:       "**/*.xml",
			wantNil: true,
		},
		{
			name:    "different markdown/text extensions never collide",
			a:       "**/*.txt",
			b:       "**/*.md",
			wantNil: true,
		},
		{
			name:     "json5 rides along with the json brace form",
			a:        "**/*.{json,json5}",
			b:        "**/*.json",
			accept:   acceptJSON,
			wantPath: ".json",
			wantKind: KindAShadowsB,
		},
		{
			name:     "mapper rule is contained by the plain xml rule",
			a:        "**/*{Mapper,Dao}*.xml",
			b:        "**/*.xml",
			accept:   acceptXML,
			wantPath: "Dao.xml",
			wantKind: KindAShadowsB,
		},
		{
			name:     "identical patterns still report (A wins by order)",
			a:        "**/*.go",
			b:        "**/*.go",
			accept:   func(string) bool { return true },
			wantPath: ".go",
			wantKind: KindAShadowsB,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := FindOverlap(Parse(tc.a), Parse(tc.b), tc.accept)
			if tc.wantNil {
				if w != nil {
					t.Fatalf("expected no overlap, got %+v", w)
				}
				return
			}
			if w == nil {
				t.Fatalf("expected an overlap")
			}
			if w.Confidence != ConfidenceExact {
				t.Fatalf("expected an exact witness, got %+v", w)
			}
			if w.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", w.Kind, tc.wantKind)
			}
			if w.Path != tc.wantPath {
				t.Fatalf("witness = %q, want %q", w.Path, tc.wantPath)
			}
			if !Parse(tc.a).Match(w.Path) || !Parse(tc.b).Match(w.Path) {
				t.Fatalf("witness %q must match both patterns", w.Path)
			}
			if w.Verified == 0 {
				t.Fatalf("a verified witness must report at least one checked path")
			}
		})
	}
}

// TestFindOverlapUnknownWhenNoWitness covers the honest-reporting path: two
// patterns that provably overlap, but for which this version cannot construct an
// admissible path. The finding must be reported as unproven, never as exact.
func TestFindOverlapUnknownWhenNoWitness(t *testing.T) {
	acceptNothing := func(string) bool { return false }
	w := FindOverlap(Parse("**/pom.xml"), Parse("**/*.xml"), acceptNothing)
	if w == nil {
		t.Fatalf("expected a reported overlap")
	}
	if w.Confidence != ConfidenceUnknown {
		t.Fatalf("confidence = %q, want %q", w.Confidence, ConfidenceUnknown)
	}
	if w.Path != "" {
		t.Fatalf("unproven overlap must not claim a path, got %q", w.Path)
	}
}
