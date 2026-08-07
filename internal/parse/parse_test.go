package parse

import (
	"strconv"
	"testing"
)

func assertMetaEqual(t *testing.T, got, want []Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestEventText pins the join every body-tag derivation shares. The separator
// matters: without it a title ending in text and a body starting with `@name`
// would fuse into one token and the mention would go unseen by one caller and
// seen by another, depending on which built the string by hand.
func TestEventText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		title, body  string
		want         string
		wantBodyTags []Meta
	}{
		{name: "both", title: "standup", body: "with @sarah", want: "standup with @sarah",
			wantBodyTags: []Meta{{Key: "people", Value: "sarah"}}},
		{name: "empty body", title: "ship #ops", body: "", want: "ship #ops ",
			wantBodyTags: []Meta{{Key: "tag", Value: "ops"}}},
		{name: "sigil at the boundary", title: "ship", body: "@sarah reviewed", want: "ship @sarah reviewed",
			wantBodyTags: []Meta{{Key: "people", Value: "sarah"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := EventText(tt.title, tt.body)
			if got != tt.want {
				t.Errorf("EventText(%q, %q) = %q, want %q", tt.title, tt.body, got, tt.want)
			}
			assertMetaEqual(t, BodyTags(got), tt.wantBodyTags)
		})
	}
}

func TestBodyTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want []Meta
	}{
		{
			name: "hash tags",
			text: "Meeting about #planning and #budget",
			want: []Meta{
				{Key: "tag", Value: "planning"},
				{Key: "tag", Value: "budget"},
			},
		},
		{
			name: "at tags",
			text: "Talked with @sarah and @bob",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "people", Value: "bob"},
			},
		},
		{
			name: "mixed tags with people first",
			text: "Discussed #planning with @sarah",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "planning"},
			},
		},
		{
			name: "hierarchical tags",
			text: "Working on #work/project-x and #infra/deploy-v2",
			want: []Meta{
				{Key: "tag", Value: "work/project-x"},
				{Key: "tag", Value: "infra/deploy-v2"},
			},
		},
		{
			name: "unicode person name is kept whole",
			text: "coffee with @josé and @田中",
			want: []Meta{
				{Key: "people", Value: "josé"},
				{Key: "people", Value: "田中"},
			},
		},
		{
			name: "unicode tag is kept whole",
			text: "playing with the #niño and #zoë",
			want: []Meta{
				{Key: "tag", Value: "niño"},
				{Key: "tag", Value: "zoë"},
			},
		},
		{
			name: "accented names that share an ASCII prefix stay distinct",
			text: "@josé and @josa",
			want: []Meta{
				{Key: "people", Value: "josé"},
				{Key: "people", Value: "josa"},
			},
		},
		{
			name: "email address does not mint a person",
			text: "emailed bob@example.com about the thing",
			want: nil,
		},
		{
			name: "url fragment does not mint a tag",
			text: "see https://docs.example.com/guide#installation",
			want: nil,
		},
		{
			name: "sigil after a non-name rune still counts",
			text: "spoke to (@alice)\nabout [#ops]",
			want: []Meta{
				{Key: "people", Value: "alice"},
				{Key: "tag", Value: "ops"},
			},
		},
		{
			name: "sigil at start of text still counts",
			text: "@alice opened #ops",
			want: []Meta{
				{Key: "people", Value: "alice"},
				{Key: "tag", Value: "ops"},
			},
		},
		{
			name: "adjacent sigils are not both tags",
			text: "#a#b",
			want: []Meta{
				{Key: "tag", Value: "a"},
			},
		},
		{
			name: "no tags",
			text: "Just a plain text entry",
			want: nil,
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
		{
			name: "duplicate tags are deduplicated",
			text: "#planning and #planning again #planning",
			want: []Meta{
				{Key: "tag", Value: "planning"},
			},
		},
		{
			name: "duplicate at tags are deduplicated",
			text: "@sarah and @sarah again",
			want: []Meta{
				{Key: "people", Value: "sarah"},
			},
		},
		{
			name: "mixed duplicates across types",
			text: "@sarah #ops @sarah #ops",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "ops"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertMetaEqual(t, BodyTags(tt.text), tt.want)
		})
	}
}

func TestKeyValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantKey   string
		wantValue string
		wantErr   bool
	}{
		{name: "ok", input: "env=prod", wantKey: "env", wantValue: "prod"},
		{name: "value contains =", input: "note=a=b", wantKey: "note", wantValue: "a=b"},
		{name: "key MetaNameRe would refuse", input: "ticket.id=PROJ-42", wantKey: "ticket.id", wantValue: "PROJ-42"},
		// Storability is not KeyValue's job — untag and meta delete split the
		// same way to name a row an older build wrote. See TestValidateMeta.
		{name: "empty value splits", input: "tag=", wantKey: "tag", wantValue: ""},
		{name: "whitespace key splits", input: "a b=c", wantKey: "a b", wantValue: "c"},
		{name: "missing =", input: "noeq", wantErr: true},
		{name: "empty input", input: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k, v, err := KeyValue(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("KeyValue(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("KeyValue(%q) unexpected error: %v", tt.input, err)
			}
			if k != tt.wantKey || v != tt.wantValue {
				t.Errorf("KeyValue(%q) = (%q, %q), want (%q, %q)", tt.input, k, v, tt.wantKey, tt.wantValue)
			}
		})
	}
}

// TestIsFilterDelim pins the shared predicate directly rather than through
// ValidateMeta, since its other caller is the -S tokenizer in another package
// and the whole point of the function is that the two agree. '!' is the row
// that matters: it is not a delimiter — a term under way absorbs it — even
// though a leading one negates.
func TestIsFilterDelim(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		r    rune
		want bool
	}{
		{' ', true}, {'\t', true}, {'\n', true}, {' ', true},
		{'&', true}, {'|', true},
		{'!', false}, {'a', false}, {'=', false}, {'.', false}, {'é', false},
	} {
		t.Run(strconv.QuoteRune(tt.r), func(t *testing.T) {
			t.Parallel()
			if got := IsFilterDelim(tt.r); got != tt.want {
				t.Errorf("IsFilterDelim(%q) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

// TestValidateMeta pins the rule directly, since it is what every entry point
// that mints a tuple now shares. The accepted rows matter as much as the
// rejected ones: the rule is "exactly one -S term", deliberately looser than
// MetaNameRe, and narrowing it would route a key --meta happily stores to a
// filter column that cannot answer for it.
//
// The rejected keys are the three ways a term ends or flips: whitespace, the
// two boolean operators, and a leading '!' — which does not merely fail to
// match but answers with the complement of what was typed.
func TestValidateMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{name: "plain", key: "env", value: "prod"},
		{name: "dotted key", key: "ticket.id", value: "PROJ-42"},
		{name: "value with spaces", key: "author", value: "Ada Lovelace"},
		{name: "unicode key", key: "réf", value: "x"},
		// '!' ends no term once one is under way, so it is legal anywhere but
		// the front — and the value is never tokenized at all.
		{name: "bang inside key", key: "a!b", value: "c"},
		{name: "operators in value", key: "k", value: "a&b|c"},
		{name: "bang leading value", key: "k", value: "!v"},

		{name: "empty key", key: "", value: "v", wantErr: true},
		{name: "empty value", key: "k", value: "", wantErr: true},
		{name: "both empty", key: "", value: "", wantErr: true},
		{name: "space in key", key: "a b", value: "c", wantErr: true},
		{name: "tab in key", key: "a\tb", value: "c", wantErr: true},
		{name: "newline in key", key: "a\nb", value: "c", wantErr: true},
		{name: "ampersand in key", key: "a&b", value: "c", wantErr: true},
		{name: "pipe in key", key: "a|b", value: "c", wantErr: true},
		{name: "bang leading key", key: "!k", value: "v", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateMeta(tt.key, tt.value)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateMeta(%q, %q) expected error", tt.key, tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateMeta(%q, %q) unexpected error: %v", tt.key, tt.value, err)
			}
		})
	}
}

func TestFlagMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   []string
		want    []Meta
		wantErr bool
	}{
		{
			name:  "single flag",
			flags: []string{"env=prod"},
			want:  []Meta{{Key: "env", Value: "prod"}},
		},
		{
			name:  "multiple flags",
			flags: []string{"env=prod", "region=us-east-1"},
			want: []Meta{
				{Key: "env", Value: "prod"},
				{Key: "region", Value: "us-east-1"},
			},
		},
		{
			name:    "missing equals sign",
			flags:   []string{"invalidflag"},
			wantErr: true,
		},
		{
			name:  "value with equals signs",
			flags: []string{"note=a=b=c"},
			want:  []Meta{{Key: "note", Value: "a=b=c"}},
		},
		{
			name:    "empty key rejected",
			flags:   []string{"=value"},
			wantErr: true,
		},
		{
			name:    "empty value rejected",
			flags:   []string{"key="},
			wantErr: true,
		},
		{
			name:    "whitespace in key rejected",
			flags:   []string{"a b=c d"},
			wantErr: true,
		},
		{
			name:  "empty flags",
			flags: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FlagMeta(tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FlagMeta(%v) expected error, got nil", tt.flags)
				}
				return
			}
			if err != nil {
				t.Fatalf("FlagMeta(%v) unexpected error: %v", tt.flags, err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}

// TestFTSColumns pins the two strings the running code indexes. The
// pre-migration-6 join of them is db.legacyFTSContent's business now, and is
// tested there.
func TestFTSColumns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		title       string
		body        string
		meta        []Meta
		wantContent string
		wantMeta    string
	}{
		{"empty", "", "", nil, "", ""},
		{"title only", "hello", "", nil, "hello", ""},
		{"body only", "", "world", nil, "world", ""},
		{"title + body", "hello", "world", nil, "hello world", ""},
		{
			"title + body + meta",
			"hello", "world",
			[]Meta{{Key: "tag", Value: "ops"}, {Key: "people", Value: "sarah"}},
			"hello world", "tag=ops people=sarah",
		},
		{
			"empty title with meta",
			"", "world",
			[]Meta{{Key: "tag", Value: "ops"}},
			"world", "tag=ops",
		},
		{
			// The M7 case: the text says what a tag would, and the two must
			// still land in different columns.
			"body quoting a meta token",
			"note", "mentions tag=ops literally",
			nil,
			"note mentions tag=ops literally", "",
		},
		{
			"meta with no text at all",
			"", "",
			[]Meta{{Key: "author", Value: "nico"}},
			"", "author=nico",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, meta := FTSColumns(tt.title, tt.body, tt.meta)
			if content != tt.wantContent || meta != tt.wantMeta {
				t.Errorf("FTSColumns(%q, %q, %v) = (%q, %q), want (%q, %q)",
					tt.title, tt.body, tt.meta, content, meta, tt.wantContent, tt.wantMeta)
			}
		})
	}
}

func TestMetaArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{name: "people", input: "@Sarah", wantKey: "people", wantVal: "Sarah"},
		{name: "tag", input: "#ops", wantKey: "tag", wantVal: "ops"},
		{name: "key=value", input: "env=prod", wantKey: "env", wantVal: "prod"},
		{name: "value with =", input: "note=a=b", wantKey: "note", wantVal: "a=b"},
		{name: "hierarchical tag", input: "#work/project-x", wantKey: "tag", wantVal: "work/project-x"},
		{name: "value with spaces", input: "author=Ada Lovelace", wantKey: "author", wantVal: "Ada Lovelace"},
		{name: "unicode person", input: "@josé", wantKey: "people", wantVal: "josé"},
		{name: "unicode tag", input: "#déploiement", wantKey: "tag", wantVal: "déploiement"},
		{name: "cjk person", input: "@田中", wantKey: "people", wantVal: "田中"},

		{name: "bare word", input: "urgent", wantErr: true},
		{name: "lone @", input: "@", wantErr: true},
		{name: "lone #", input: "#", wantErr: true},
		{name: "@ with space", input: "@ Sarah", wantErr: true},
		{name: "missing key", input: "=value", wantErr: true},
		// Accepted so `event untag 'k='` / `meta delete 'k='` can still name
		// a row an older build stored. Minting is gated at the writer.
		{name: "empty value names an existing row", input: "k=", wantKey: "k", wantVal: ""},
		{name: "whitespace key names an existing row", input: "a b=c", wantKey: "a b", wantVal: "c"},
		{name: "empty input", input: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MetaArg(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("MetaArg(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("MetaArg(%q) err = %v", tt.input, err)
			}
			if got.Key != tt.wantKey || got.Value != tt.wantVal {
				t.Errorf("MetaArg(%q) = (%q, %q), want (%q, %q)",
					tt.input, got.Key, got.Value, tt.wantKey, tt.wantVal)
			}
		})
	}
}

func TestSplitTitleBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		title string
		body  string
	}{
		{"", "", ""},
		{"   ", "", ""},
		{"hello", "hello", ""},
		{"hello.", "hello.", ""},
		{"hello. world", "hello", "world"},
		{"  hello  .  world  ", "hello", "world"},
		{"v1.2 done. Hotfix", "v1.2 done", "Hotfix"},
		{"v1.2.3 released", "v1.2.3 released", ""},
		{"first. second. third", "first", "second. third"},
		{". hello", "", "hello"},
		{"hello. ", "hello", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			gotTitle, gotBody := SplitTitleBody(tt.input)
			if gotTitle != tt.title || gotBody != tt.body {
				t.Errorf("SplitTitleBody(%q) = (%q, %q), want (%q, %q)",
					tt.input, gotTitle, gotBody, tt.title, tt.body)
			}
		})
	}
}
