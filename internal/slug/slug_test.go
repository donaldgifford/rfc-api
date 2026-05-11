package slug

import "testing"

// TestSlug covers the divergence classes called out in INV-0002 #Findings.
// Each row corresponds to a category of heading content where the old
// rfc-api slugify diverged from github-slugger. Keep the comment column
// in sync with the doc so future maintainers can trace a case back to
// its rationale.
func TestSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in, out string
	}{
		// --- ASCII basics ---
		{"ascii_simple_heading", "Simple heading", "simple-heading"},
		{"ascii_single_word", "Simple", "simple"},
		{"ascii_punctuation_kept_around_word", "Hello, World!", "hello-world"},
		{"ascii_already_kebab", "one-two-three", "one-two-three"},
		{"digits_only", "2026", "2026"},
		{"single_letter", "a", "a"},

		// --- Apostrophe: stripped, not hyphenated (rfc-api used to emit "what-s-new"). ---
		{"apostrophe_stripped", "What's New?", "whats-new"},

		// --- Period: stripped between digits/letters (rfc-api used to emit "a-b-c"). ---
		{"periods_between_letters", "A.B.C", "abc"},
		{"period_in_version_then_space", "Section 1.2.3", "section-123"},

		// --- Ampersand stripped (rfc-api used to emit "q-a"). ---
		{"ampersand_stripped", "Q&A", "qa"},

		// --- Em dash stripped (rfc-api used to emit "one-two" via dash substitution). ---
		{"em_dash_stripped", "One—two", "onetwo"},
		{"em_dash_with_spaces", "rfd-api — what it does", "rfd-api--what-it-does"},

		// --- Underscore is kept (rfc-api used to strip and replace with hyphen). ---
		{"underscore_inner_kept", "my_var", "my_var"},
		{"underscore_leading_trailing_kept", "__init__", "__init__"},

		// --- Whitespace handling: no collapse, no trim. ---
		{"multiple_consecutive_spaces", "Hello   World", "hello---world"},
		{"leading_trailing_space", "  Padded  ", "--padded--"},
		{"whitespace_only", "   ", "---"},
		{"tab_stripped", "foo\tbar", "foobar"},

		// --- Unicode letters preserved via \p{L}. ---
		{"latin1_accent", "Café", "café"},
		{"latin_extended_naive", "naïve", "naïve"},
		{"latin_extended_manana", "Mañana", "mañana"},
		{"cjk_japanese", "日本語", "日本語"},
		{"cjk_chinese", "中文", "中文"},
		{"cjk_korean", "한국어", "한국어"},
		{"cyrillic", "Привет", "привет"},
		{"greek_with_hyphens", "α-β-γ", "α-β-γ"},

		// --- Operator-y heading characters all strip cleanly. ---
		{"percent_stripped", "100%", "100"},
		{"plus_stripped", "C++", "c"},
		{"brackets_stripped", "[Link]", "link"},
		{"parens_stripped", "(parens)", "parens"},

		// --- The 'X / Y' separator pattern (common in real corpus). ---
		{"slash_with_surrounding_spaces", "OpenAPI / contract management", "openapi--contract-management"},

		// --- Boundary cases. ---
		{"empty_string", "", ""},
		{"only_stripped_chars", "!!!", ""},
		{"only_keep_chars_post_lower", "AB_C", "ab_c"},

		// --- Post-AST view of a heading containing inline code. ---
		// The goldmark walker drops backticks; this is what slug sees.
		{"post_ast_inline_code", "The Source field", "the-source-field"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Slug(c.in); got != c.out {
				t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.out)
			}
		})
	}
}

func TestSlugger_FreshInstance_FirstCallReturnsBareSlug(t *testing.T) {
	t.Parallel()
	g := NewSlugger()
	if got := g.Slug("Notes"); got != "notes" {
		t.Errorf("first call: got %q, want %q", got, "notes")
	}
}

func TestSlugger_RepeatedSameInput_SuffixesIncrementally(t *testing.T) {
	t.Parallel()
	g := NewSlugger()
	want := []string{"notes", "notes-1", "notes-2", "notes-3"}
	for i, w := range want {
		if got := g.Slug("Notes"); got != w {
			t.Errorf("call #%d: got %q, want %q", i+1, got, w)
		}
	}
}

// TestSlugger_PreExistingSuffix_AdvancesPastIt pins the edge case the
// upstream while-loop handles: if the caller manually feeds a slug that
// equals an existing collision suffix, the next collision must advance
// past it, not collide again. See IMPL-0006 Phase 2 success criteria.
func TestSlugger_PreExistingSuffix_AdvancesPastIt(t *testing.T) {
	t.Parallel()
	g := NewSlugger()

	// Step 1: bare "notes" is recorded.
	if got := g.Slug("Notes"); got != "notes" {
		t.Fatalf("step 1: got %q, want %q", got, "notes")
	}
	// Step 2: caller manually feeds "Notes-1" — recorded as-is.
	if got := g.Slug("Notes-1"); got != "notes-1" {
		t.Fatalf("step 2: got %q, want %q", got, "notes-1")
	}
	// Step 3: another "Notes" — must skip past the existing notes-1 to notes-2.
	if got := g.Slug("Notes"); got != "notes-2" {
		t.Errorf("step 3: got %q, want %q (must not collide with notes-1)", got, "notes-2")
	}
}

func TestSlugger_InstanceIsolation(t *testing.T) {
	t.Parallel()
	a, b := NewSlugger(), NewSlugger()
	if got := a.Slug("Notes"); got != "notes" {
		t.Errorf("a: got %q, want %q", got, "notes")
	}
	if got := b.Slug("Notes"); got != "notes" {
		t.Errorf("b: got %q, want %q (instances must not share state)", got, "notes")
	}
}

// TestSlugger_DistinctInputs_NoCollision asserts the slugger does not
// add suffixes for headings that happen to slug to different values.
func TestSlugger_DistinctInputs_NoCollision(t *testing.T) {
	t.Parallel()
	g := NewSlugger()
	want := []struct {
		in, out string
	}{
		{"Alpha", "alpha"},
		{"Beta", "beta"},
		{"Gamma", "gamma"},
	}
	for _, w := range want {
		if got := g.Slug(w.in); got != w.out {
			t.Errorf("Slug(%q) = %q, want %q (distinct inputs must not suffix)",
				w.in, got, w.out)
		}
	}
}
