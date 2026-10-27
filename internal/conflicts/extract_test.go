package conflicts

import (
	"math/big"
	"strings"
	"testing"
)

func TestExtractKinds(t *testing.T) {
	for _, tt := range []struct{ text, kind string }{{"$7", "money"}, {"7%", "percent"}, {"2026-07-01", "date"}, {"Fee $7 日本語", "money"}} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Kind != tt.kind || got[0].Text != string([]rune(tt.text)[got[0].Start:got[0].End]) {
			t.Errorf("typed figure %q: %+v", tt.text, got)
		}
	}
	if got := Extract("料金 $7"); len(got) != 0 {
		t.Errorf("non-Latin clause: %+v", got)
	}

	if g := Extract("Cost is 7€."); len(g) != 1 || g[0].Unit != "€" {
		t.Fatalf("currency suffix: %+v", g)
	}

	text := "The fee is $1,000.50. The wait is 2 days."
	got := Extract(text)
	if len(got) != 2 {
		t.Fatalf("got %d figures: %+v", len(got), got)
	}
	if got[0].Value != "1000.5" || got[0].Text != "$1,000.50" || got[1].Value != "2880" {
		t.Fatalf("unexpected figures: %+v", got)
	}
	for _, f := range got {
		if string([]rune(text)[f.Start:f.End]) != f.Text {
			t.Fatalf("bad span: %+v", f)
		}
	}

	tests := []struct{ text, kind, unit, value, end, qualifier string }{
		{"Fee is 2 percent.", "percent", "percent", "2", "", ""},
		{"Fee is 2%.", "percent", "percent", "2", "", ""},
		{"Rate rose 2 percentage points.", "percent", "percentage point", "2", "", ""},
		{"Wait is between 7 and 10 business days.", "duration", "business days", "7", "10", ""},
		{"Wait is 1 year.", "duration", "months", "12", "", ""},
		{"Wait is zero days.", "duration", "minutes", "0", "", ""},
		{"Wait is one day.", "duration", "minutes", "1440", "", ""},
		{"Wait is twenty days.", "duration", "minutes", "28800", "", ""},
		{"There are one free relocation.", "count", "relocation", "1", "", ""},
		{"Policy number: AB-1234 applies.", "identifier", "policy", "ab1234", "", ""},
		{"Effective date is 1 July 2026.", "date", "date", "2026-07-01", "", ""},
		{"Effective date is July 2026.", "date", "date", "2026-07", "", ""},
		{"Effective date is 13/07/2026.", "date", "date", "2026-07-13", "", ""},
		{"Cost is EUR 7.", "money", "eur", "7", "", ""}, {"Cost is 7 euros.", "money", "euro", "7", "", ""},
		{"Cost is 7 rubles.", "money", "rouble", "7", "", ""},
		{"Wait is 3 min.", "duration", "minutes", "3", "", ""}, {"Wait is 2 weeks.", "duration", "minutes", "20160", "", ""},
		{"Start is 2026-07-01.", "date", "date", "2026-07-01", "", ""}, {"Start is July 1, 2026.", "date", "date", "2026-07-01", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			g := Extract(tt.text)
			if len(g) != 1 {
				t.Fatalf("got %+v", g)
			}
			f := g[0]
			if f.Kind != tt.kind || f.Unit != tt.unit || f.Value != tt.value || f.EndValue != tt.end || f.Qualifier != tt.qualifier {
				t.Fatalf("got %+v", f)
			}
		})
	}
	for _, adjective := range []string{"free", "paid"} {
		g := Extract("There are 2 " + adjective + " devices.")
		if len(g) != 1 || g[0].Modifier != adjective {
			t.Fatalf("modifier %q: %+v", adjective, g)
		}
	}
}
func TestExtractConservative(t *testing.T) {
	for _, text := range []string{"The wait is not\n\n7 days.", "- The wait is not\n- Wait is 7 days.", "Header | Value\nWait | 7 days\nFee is $5."} {
		got := Extract(text)
		if len(got) != 1 || (got[0].Text != "7 days" && got[0].Text != "$5") {
			t.Errorf("hard boundary %q: %+v", text, got)
		}
	}
	for _, text := range []string{"The wait is not\n7 days.", "The wait is approximately\n7 days."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("wrapped qualifier %q: %+v", text, got)
		}
	}
	t.Run("give or take", func(t *testing.T) {
		for _, text := range []string{"The wait is 7 days and 3 hours, give or take 1 hour.", "The wait is 7 days and 3 hours, give or take 1 day."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
		for _, figure := range []string{"7 days", "$7", "7 percent", "7 devices"} {
			text := "The value is " + figure + ", give or take 1 day."
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
		text := "The wait is ７ days, give or take 1 day and the fee is $5 per 2 devices."
		got := Extract(text)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Fatalf("independent rate: %+v", got)
		}
		for _, f := range got {
			if string([]rune(text)[f.Start:f.End]) != f.Text {
				t.Fatalf("source offset: %+v", f)
			}
		}
		if got := Extract("The wait is 7 days (standard service)."); len(got) != 1 || got[0].Text != "7 days" {
			t.Fatalf("parenthetical prose: %+v", got)
		}
	})

	for _, s := range []string{"About 7 days apply.", "This is not 7 days.", "For example 7 days apply.", "row | 7 days | cell", "Plan | Wait\n--- | ---\nBasic | 7 days", "Policy: XXXX1234 applies.", "The date is 07/01/2026.", "Plan 59 applies."} {
		if got := Extract(s); len(got) != 0 {
			t.Errorf("%q: %+v", s, got)
		}
	}
	for _, text := range []string{
		"The wait is (7 days) approximately.", `The wait is "7 days" approximately.`,
		"The wait is 7 days and 3 hours (approximately).", "The wait is 7 days (approximately) and 3 hours.", "The wait is (approximately) 7 days, 3 hours.",
		"The wait is 7 days and 3 hours approximately.", "The wait is 7 days, 3 hours approximately.",
		"The wait is about 7 days and 3 hours.",
		"The wait is 7 days approximately and 3 hours.",
		"The wait is 3 hours and about 7 days.",
		"The wait is about 7 days and\t3 hours.",
		"The wait is about 7 days and (3 hours).",
	} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("approximate duration %q: %+v", text, got)
		}
	}

	f := Extract("From 1 July 2024 to 30 June 2026 the wait is 7 days.")
	if len(f) != 1 || f[0].Period.From != "2024-07-01" || f[0].Period.To != "2026-06-30" {
		t.Fatalf("period: %+v", f)
	}
	f = Extract("… the wait is 7 days …")
	if len(f) != 1 || !f[0].Partial {
		t.Fatalf("partial: %+v", f)
	}
	t.Run("NegatedContractions", func(t *testing.T) {
		for _, apostrophe := range []string{"'", "’", "‘", "ʼ"} {
			t.Run(apostrophe, func(t *testing.T) {
				clause := "The customer" + apostrophe + "s cooling period is 15 days."
				text := "The cooling period isn" + apostrophe + "t 7 days. " + clause
				got := Extract(text)
				if len(got) != 1 || got[0].Text != "15 days" || got[0].Clause != clause {
					t.Fatalf("negated contraction extracted or source quote changed: %+v", got)
				}
				runes := []rune(text)
				f := got[0]
				if string(runes[f.Start:f.End]) != "15 days" || string(runes[f.ClauseStart:f.ClauseEnd]) != clause {
					t.Fatalf("original offsets changed: %+v", f)
				}
			})
		}
	})
	t.Run("SkipsCannotNegation", func(t *testing.T) {
		text := "The wait cannot be 7 days. The wait is 2 days."
		got := Extract(text)
		if len(got) != 1 || got[0].Text != "2 days" || got[0].Clause != "The wait is 2 days." {
			t.Fatalf("cannot emitted an affirmative figure: %+v", got)
		}
		if got := Extract("The cannotary wait is 7 days."); len(got) != 1 {
			t.Fatalf("negation must match a whole word: %+v", got)
		}
	})
	t.Run("SkipsStandaloneNoAndWithout", func(t *testing.T) {
		for _, text := range []string{"There is no $7 fee.", "The policy is without a 7 day waiting period."} {
			t.Run(text, func(t *testing.T) {
				if got := Extract(text); len(got) != 0 {
					t.Fatalf("negated assertion retained: %+v", got)
				}
			})
		}
	})
	t.Run("SkipsApproximateExpressions", func(t *testing.T) {
		for _, text := range []string{
			"The wait is between about 7 and 10 days.",
			`The wait is about "7 days".`,
			"The wait is about ‘7 days’.",
			"The wait is approximately at least 7 days.",
			"The wait is approximately ≤ 7 days.",
			"The fee is between approximately 7 and 10 dollars.",
			"The rate is between nearly 7 and 10 percent.",
			"There are between roughly 7 and 10 devices.",
			"The wait is about 7 to 10 days.",
			"The wait is between 7 and about 10 days.",
		} {
			t.Run(text, func(t *testing.T) {
				if got := Extract(text); len(got) != 0 {
					t.Fatalf("approximate expression retained precise components: %+v", got)
				}
			})
		}
		{
			if got := Extract("The fee is between about $7 and $10."); len(got) != 0 {
				t.Fatalf("%+v", got)
			}
		}
		for _, w := range []string{"about", "approximately", "around", "roughly", "nearly", "almost", "~"} {
			if g := Extract("Wait is " + w + " 7 days."); len(g) != 0 {
				t.Errorf("%q: %+v", w, g)
			}
		}
		for _, figure := range []string{"7 days", "$５", "7 percent", "7 devices", "7-10 days"} {
			for _, phrase := range []string{"approximately a total of", "around a total of", "AROUND  A TOTAL OF"} {
				text := "The value is " + phrase + " " + figure + "."
				if got := Extract(text); len(got) != 0 {
					t.Errorf("%q: %+v", text, got)
				}
			}
		}
		for _, text := range []string{"The wait is approximately a total of 7 days and 3 hours.", "The wait is approximately a total of ７ days and 3 hours and the fee is $5 per 2 devices."} {
			got := Extract(text)
			if strings.Contains(text, "$5") {
				if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" || string([]rune(text)[got[0].Start:got[0].End]) != "$5" {
					t.Errorf("%q: %+v", text, got)
				}
			} else if len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
		if got := Extract("The total is a total of 7 days."); len(got) != 1 || got[0].Text != "7 days" {
			t.Errorf("exact total: %+v", got)
		}
		for _, figure := range []string{"7 days", "$７", "7 percent", "7 devices", "7-10 days"} {
			for _, modifier := range []string{"almost exactly", "ALMOST  EXACTLY", "about exactly"} {
				text := "The value is " + modifier + " " + figure + "."
				if got := Extract(text); len(got) != 0 {
					t.Errorf("%q: %+v", text, got)
				}
			}
		}
		for _, text := range []string{"The wait is almost exactly 7 days and 3 hours.", "The wait is almost exactly ７ days and 3 hours and the fee is $5 per 2 devices."} {
			got := Extract(text)
			if strings.Contains(text, "$5") {
				if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" || string([]rune(text)[got[0].Start:got[0].End]) != "$5" {
					t.Errorf("%q: %+v", text, got)
				}
			} else if len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
		if got := Extract("The wait is exactly 7 days."); len(got) != 1 || got[0].Text != "7 days" {
			t.Errorf("exact: %+v", got)
		}
		for _, figure := range []string{"$5", "7 days", "7 percent", "7 devices"} {
			for _, text := range []string{"The value is " + figure + " (approximately).", "The value is (approximately) " + figure + ".", "The value is approximately (" + figure + ")."} {
				if got := Extract(text); len(got) != 0 {
					t.Errorf("%q: %+v", text, got)
				}
			}
		}
		text := "The rate is $5 per 2 devices and the fee is about $10."
		got := Extract(text)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Fatalf("independent rate: %+v", got)
		}
		text = "The wait is ７ days and ３ hours (approximately) and the fee is $5 per 2 devices."
		got = Extract(text)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Fatalf("independent rate: %+v", got)
		}
		for _, f := range got {
			if string([]rune(text)[f.Start:f.End]) != f.Text {
				t.Fatalf("source offset: %+v", f)
			}
		}
		text = "The wait is ７ days and ３ hours approximately and the fee is $5 per 2 devices."
		got = Extract(text)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Fatalf("independent rate: %+v", got)
		}
		for _, f := range got {
			if string([]rune(text)[f.Start:f.End]) != f.Text {
				t.Fatalf("source offset: %+v", f)
			}
		}
		for _, tt := range []struct{ text, want string }{
			{"The wait is 7 days and 3 hours approximately and the timeout is 2 days.", "2 days"},
			{"The fee is $5 and the wait is approximately 7 days, 3 hours.", "$5"},
			{"The fee is about $5 and the charge is $10.", "$10"},
			{"The rate is about 5 percent and the charge is 10 percent.", "10 percent"},
			{"The wait is about 7 days and the fee is $5.", "$5"},
			{"The wait is about 7 days, $3 fee.", "$3"},
		} {
			got := Extract(tt.text)
			if len(got) != 1 || got[0].Text != tt.want || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.want {
				t.Errorf("independent fact %q: %+v", tt.text, got)
			}
		}
		for _, text := range []string{
			"The fees are about $5 and $10.", "The fees are $5 and $10 approximately.",
			"The rates are about 5 percent and 10 percent.", "There are about 2 devices and 3 users.",
			`The wait is about "7 days" and "3 hours".`, `The fees are about "$5" and "$10".`, `The rates are about "5 percent" and "10 percent".`, `There are about "2 devices" and "3 users".`,
		} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("shared approximation %q: %+v", text, got)
			}
		}
		if got := Extract("The wait is about 7 days, 3 hours."); len(got) != 0 {
			t.Errorf("approximate comma duration: %+v", got)
		}
		for _, figure := range []string{"$7", "7 days", "7 percent", "7 devices", "1 July 2026", "Policy: AB1234"} {
			for _, text := range []string{"The value is about: " + figure + ".", "The value is CIRCA : " + figure + ".", "The value is " + figure + ", estimated."} {
				if got := Extract(text); len(got) != 0 {
					t.Errorf("%q: %+v", text, got)
				}
			}
		}
		for _, text := range []string{"The wait is circa 7 days and 3 hours.", "The wait is 7 days and 3 hours, estimated."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
		text = "The wait is CIRCA: ７ days and 3 hours and the fee is $5 per 2 devices."
		got = Extract(text)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Errorf("%q: %+v", text, got)
		} else {
			for _, f := range got {
				if string([]rune(text)[f.Start:f.End]) != f.Text {
					t.Errorf("bad span: %+v", f)
				}
			}
		}
		for _, marker := range []string{"about", "approximately", "around", "roughly", "nearly", "almost", "~", ", approximately"} {
			for _, figure := range []string{"7 days", "$7", "7 percent", "7 devices", "1 July 2026", "Policy: AB1234"} {
				t.Run(figure+" "+marker, func(t *testing.T) {
					if got := Extract("The value is " + figure + " " + marker + "."); len(got) != 0 {
						t.Fatalf("approximate figure treated as exact: %+v", got)
					}
				})
			}
		}
	})
}

func TestExtractASCIIDotRunEllipses(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		partial    []bool
	}{
		{"first fragment", "The period is 7 days...", []bool{true}},
		{"unicode newline", "…\nThe wait is 7 days.", []bool{true}},
		{"empty lines", "...\n\nThe wait is 7 days.", []bool{true}},
		{"last fragment", "...The period is 7 days.", []bool{true}},
		{"joined fragments", "The period is 7 days... The fee is $5.", []bool{true, true}},
		{"longer dot run", "The period is 7 days.... The fee is $5.", []bool{true, true}},
		{"complete surrounding clauses", "The wait is 1 day. ... The period is 7 days. The fee is $5.", []bool{false, true, false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.text)
			if len(got) != len(tt.partial) {
				t.Fatalf("figures: %+v", got)
			}
			runes := []rune(tt.text)
			for i, f := range got {
				if f.Partial != tt.partial[i] || string(runes[f.Start:f.End]) != f.Text || string(runes[f.ClauseStart:f.ClauseEnd]) != f.Clause {
					t.Errorf("figure %d: partial or original offsets: %+v", i, f)
				}
			}
		})
	}
}
func FuzzExtract(f *testing.F) {
	f.Add("The wait is " + strings.Repeat("9", 128) + " weeks.")
	f.Add("The wait is 7 days - 10 days.")
	f.Add("The fee is $7 to about $10.")
	f.Add("Policy: XXXX-2026-07-01 applies.")
	for _, s := range []string{"The fee is $1,000.50.", "From 1 July 2026 wait 15 days.", "\xff 2 days …", "The rate is one point five percent.", "The rate is one percent point five.",
		"Wait is twenty-one days.", "Wait is thirty-one days.", "Wait is 10-7 days.",
		"Wait is 20-1 days.", "Wait is 7-10 days.", "The period isn’t 7 days.", "The period is 7 days...", "...The period is 7 days.", "From １ July ２０２６ the fee is $5 per 2 months.", "Wait is ½ day.", "Cost is $1/2.", "The wait cannot be 7 days.", "Wait is 7 days approximately.", "Account number: 1234 5678.", "The fee is $7 and 2 devices are included.", "Policy: USD1234A.", "The fee is $fourteen."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := []rune(s)
		var byteOffsets []int
		for i := range s {
			byteOffsets = append(byteOffsets, i)
		}
		byteOffsets = append(byteOffsets, len(s))
		prev := 0
		for _, v := range Extract(s) {
			if v.Start < prev || v.End < v.Start || v.End > len(r) || v.ClauseStart < 0 || v.ClauseStart > v.Start || v.ClauseEnd < v.End || v.ClauseEnd > len(r) {
				t.Fatalf("invalid span %+v", v)
			}
			if s[byteOffsets[v.Start]:byteOffsets[v.End]] != v.Text || s[byteOffsets[v.ClauseStart]:byteOffsets[v.ClauseEnd]] != v.Clause {
				t.Fatalf("bad figure span %+v", v)
			}
			if v.Kind == "money" || v.Kind == "percent" || v.Kind == "duration" || v.Kind == "count" {
				assertCanonicalNumber(t, v.Value)
				if v.EndValue != "" {
					assertCanonicalNumber(t, v.EndValue)
				}
			}
			prev = v.End
		}
	})
}

func assertCanonicalNumber(t *testing.T, value string) {
	t.Helper()
	scale := 0
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		scale = len(value) - dot - 1
		if strings.HasSuffix(value, "0") {
			t.Fatalf("non-canonical decimal %q", value)
		}
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() < 0 || r.FloatString(scale) != value {
		t.Fatalf("non-idempotent value %q", value)
	}
}

func TestExtractNormalizedValueExceedsSourceLimit(t *testing.T) {
	text := "The wait is " + strings.Repeat("9", 128) + " weeks."
	got := Extract(text)
	// 10080 * (10^128 - 1), written independently of the normalizer.
	want := "10079" + strings.Repeat("9", 123) + "89920"
	if len(got) != 1 || got[0].Value != want || got[0].Unit != "minutes" {
		t.Fatalf("source-limit normalization: %+v", got)
	}
	assertCanonicalNumber(t, got[0].Value)
}

func TestExtractSentenceEndingInAbbreviationSuffix(t *testing.T) {
	if g := Extract("Policy No. is AB1234."); len(g) != 1 || g[0].Kind != "identifier" || g[0].Value != "ab1234" || g[0].Clause != "Policy No. is AB1234." {
		t.Fatalf("abbreviation: %+v", g)
	}
	t.Run("multiple abbreviations", func(t *testing.T) {
		text := "Policy No. is AB1234 and account No. is CD5678."
		got := Extract(text)
		if len(got) != 2 || got[0].Value != "ab1234" || got[1].Value != "cd5678" || got[1].Clause != text || string([]rune(text)[got[1].Start:got[1].End]) != got[1].Text {
			t.Fatalf("multiple abbreviations: %+v", got)
		}
	})

	answer := "No. The fee is $5."
	figures := Extract(answer)
	if len(figures) != 1 || figures[0].Text != "$5" || figures[0].Clause != "The fee is $5." || string([]rune(answer)[figures[0].ClauseStart:figures[0].ClauseEnd]) != "The fee is $5." {
		t.Errorf("standalone answer: %+v", figures)
	}
	text := "This is not a casino. The wait is 7 days."
	got := Extract(text)
	if len(got) != 1 || got[0].Text != "7 days" || got[0].Clause != "The wait is 7 days." {
		t.Fatalf("abbreviation suffix merged sentences: %+v", got)
	}
}

func TestExtractDateWhitespace(t *testing.T) {
	for _, text := range []string{"As  of 1 July 2026 wait is 7 days.", "As\tof 1 July 2026 wait is 7 days."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Qualifier != "" || got[0].Period.From != "2026-07-01" {
			t.Errorf("phrase whitespace %q: %+v", text, got)
		}
	}
	for _, tt := range []struct{ date, value, precision string }{
		{"1\tJuly\t2026", "2026-07-01", "day"},
		{"July\t2026", "2026-07", "month"},
		{"July\t1,\t2026", "2026-07-01", "day"},
	} {
		t.Run(tt.date, func(t *testing.T) {
			for _, period := range []bool{false, true} {
				text := "The start date is " + tt.date + "."
				if period {
					text = "From " + tt.date + " the fee is $7."
				}
				got := Extract(text)
				if len(got) != 1 {
					t.Fatalf("date whitespace %q: %+v", text, got)
				}
				v := got[0]
				if period && (v.Text != "$7" || v.Period.From != tt.value) || !period && (v.Kind != "date" || v.Value != tt.value || v.Precision != tt.precision || v.Text != tt.date) {
					t.Fatalf("date normalization %q: %+v", text, v)
				}
				if string([]rune(text)[v.Start:v.End]) != v.Text || v.Clause != text {
					t.Fatalf("date normalization changed source: %+v", v)
				}
			}
		})
	}
}

func TestExtractRejectsContinuedNumericExpressions(t *testing.T) {
	for _, text := range []string{"The charge is two thirds of $100.", "The charge is three quarters of $100.", "The fee is $7, ±50 cents.", "The rate is 7 percent, ∓2 percent."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("unsupported expression %q: %+v", text, got)
		}
	}
	for _, numerator := range []string{"5", "five"} {
		for _, denominator := range []string{"10 devices", "ten devices", "10 days", "10 percent", "$10"} {
			text := "The fee is $3. The value is " + numerator + " out of " + denominator + ". The wait is 2 days."
			got := Extract(text)
			if len(got) != 2 || got[0].Text != "$3" || got[1].Text != "2 days" {
				t.Errorf("proportion %q: %+v", text, got)
			}
		}
	}

	for _, text := range []string{"The wait is 7 days to (10 days).", `The wait is 7 days through "10 days".`, "The deadline is 7 days after 10 days.", "The deadline is 7 days before 10 days.", "The deadline is 7 days prior to 10 days."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("relative operands %q: %+v", text, got)
		}
	}
	for _, s := range []string{"The fee is fifty dollars less $10.", "The charge is $100 less $10.", "The wait is 7 days less 2 hours."} {
		if got := Extract(s); len(got) != 0 {
			t.Errorf("%q: %+v", s, got)
		}
	}

	for _, text := range []string{
		"The wait is (7 days) or (10 days).", "The wait is ‘7 days’ or ‘10 days’.", "The wait is 7 days (or 10 days).",
		"The wait is 7 or 10 days.", "The wait is seven or ten days.",
		"The fee is 7 or 10 dollars.", "The fee is $7 or $10.",
		"There are 7 or 10 devices.", "The rate is 7 or 10 percent.",
	} {
		t.Run(text, func(t *testing.T) {
			if got := Extract(text); len(got) != 0 {
				t.Fatalf("alternatives retained an exact component: %+v", got)
			}
		})
	}

	for _, text := range []string{
		"Wait is 1/2 day.", "Wait is 1 / 2 day.", "Wait is ½ day.",
		"Cost is $1/2.", "Cost is $1 / 2.", "Cost is 1/2 dollars.",
		"Rate is 1/2 percent.", "There are 1/2 devices.",
		"The fee is $5/2 devices + $10.", "The fee is $5/month + $10.",
		"The fee is $5/" + strings.Repeat("9", 129) + " devices.",
		"Rate is 10-7 devices/5 days.", "Rate is about 2 devices/5 days.",
	} {
		t.Run(text, func(t *testing.T) {
			if got := Extract(text); len(got) != 0 {
				t.Fatalf("partial numeric expression: %+v", got)
			}
		})
	}

	t.Run("minus", func(t *testing.T) {
		for _, text := range []string{"The fee is $7 minus 50 cents.", "The wait is 7 days minus 50 seconds.", "The rate is 7 percent minus 50 basis points.", "There are 7 devices minus 50 parts."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
	})

	clauses := []string{
		"The charge is half of $100.", "The charge is a quarter of $100.",
		"The charge is 1/2 of $100.", "The charge is 1 / 2 of $100.",
		"The wait is 7 days < 10 days.", "The wait is 7 days >= 10 days.",
		"The fee is $7 to 30 June 2026/2027.",
		"The fee is 50% off$100.", "The fee is 50% off $100.", "The fee is 50 percent off $100.",
		"The fee is 5 dollars less than 10 dollars.", "The fee is 5 dollars more than 10 dollars.", "The fee is 5 dollars greater than 10 dollars.",
		"The wait is 5 days less than 10 days.", "The wait is 5 days more than 10 days.", "The wait is 5 days greater than 10 days.",
		"The fee is $7 - $10.", "The fee is $7 – $10.", "The fee is $7 + $10.",
		"The fee is 7 dollars + 10 dollars.", "The fee is $7 plus $10.",
		"The fee is fifty dollars plus $10.",
		"The fee is $7 * $10.", "The fee is $7 × $10.", "The fee is $7 ÷ $10.",
		"The wait is 7 days - 10 days.", "The wait is 7 days–10 days.",
		"The rate is 7%-10%.", "The rate is 7%–10%.", "There are 7 devices - 10 devices.",
		"The rate is 7%2.", "The wait is 7 days and a third.", "The wait is 7 days and a fifth.", "The wait is 7 days and a half.", "The rate is 7 percent and a half.",
		"The fee is $7 to about $10.", "The wait is from 7 to at most 10 days.",
		"The wait is between 7 and at most 10 days.",
		"The fee is between $7 and $10.", "The fee is $7 through about $10.",
		"The wait is 7 through 10 days.", "The wait is 7 until 10 days.", "The wait is 7 till 10 days.",
		"The fee is $7 until about $10.", "The fee is $7 till about $10.",
	}
	for _, connector := range []string{"through", "until", "till"} {
		for _, endpoints := range [][2]string{{"7 days", "10 days"}, {"$7", "$10"}, {"7 percent", "10 percent"}, {"7 devices", "10 devices"}} {
			clauses = append(clauses, "Value is "+endpoints[0]+" "+connector+" "+endpoints[1]+".")
		}
	}
	for _, dash := range []string{"—", "‐", "‒", "―"} {
		clauses = append(clauses, "The wait is 7 "+dash+" 10 days.", "The wait is 7"+dash+"10 days.", "The wait is 7 days "+dash+" 10 days.", "The fee is $7"+dash+"$10.")
	}
	for _, op := range []string{"±", "∓", "plus or minus"} {
		for _, pair := range [][2]string{{"7", "1 days"}, {"7", "1 percent"}, {"7", "1 devices"}, {"$7", "$1"}, {"7 days", "2 days"}, {"7 percent", "1 percent"}, {"7 devices", "1 devices"}} {
			clauses = append(clauses, "The value is "+pair[0]+" "+op+" "+pair[1]+".")
		}
	}
	for _, clause := range clauses {
		t.Run(clause, func(t *testing.T) {
			text := "The fee is $5. " + clause + " The wait is 2 days."
			got := Extract(text)
			if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 days" {
				t.Fatalf("unsupported expression retained exact components: %+v", got)
			}
			for _, f := range got {
				if string([]rune(text)[f.Start:f.End]) != f.Text || string([]rune(text)[f.ClauseStart:f.ClauseEnd]) != f.Clause {
					t.Fatalf("surrounding source spans changed: %+v", f)
				}
			}
		})
	}
	t.Run("RejectsPartiallyJoinedToRanges", func(t *testing.T) {
		for _, text := range []string{"Wait is 7 to10 days.", "Wait is 7to 10 days."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
	})
}

func TestExtractRejectsContinuedDates(t *testing.T) {
	for _, tt := range []struct{ text, want string }{
		{"The period is 2026-07-01/2026-07-31.", ""},
		{"Date is 2026-07-01 / 2026-07-31.", ""},
		{"From 2026-07-01 / 2026-07-31 the fee is $7.", "$7"},
		{"From July 2026 / July 2027 the fee is $7.", "$7"},
	} {
		got := Extract(tt.text)
		if tt.want == "" {
			if len(got) != 0 {
				t.Errorf("%q: %+v", tt.text, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Text != tt.want || got[0].Kind != "money" || got[0].Period != (Period{}) || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.want {
			t.Errorf("%q: %+v", tt.text, got)
		}
	}

	for _, date := range []string{"2026-07-01-02", "1 July 2026/2027", "1 July 2026_extra"} {
		t.Run(date, func(t *testing.T) {
			if got := Extract("The start date is " + date + "."); len(got) != 0 {
				t.Fatalf("continued date retained a prefix: %+v", got)
			}
			got := Extract("From " + date + " the fee is $7.")
			if len(got) != 1 || got[0].Text != "$7" || got[0].Period != (Period{}) {
				t.Fatalf("continued date became an exact period: %+v", got)
			}
		})
	}
}

func TestExtractNormalizationPreservesOriginalSpans(t *testing.T) {
	s := "Ｆｅｅ is １２ days. Cost is 7 EUR."
	got := Extract(s)
	if len(got) != 2 || got[0].Value != "17280" || got[0].Text != "１２ days" || got[1].Unit != "eur" {
		t.Fatalf("got %+v", got)
	}
	r := []rune(s)
	for _, f := range got {
		if string(r[f.Start:f.End]) != f.Text || string(r[f.ClauseStart:f.ClauseEnd]) != f.Clause {
			t.Fatalf("bad original span %+v", f)
		}
	}
}

func TestExtractQualifierVocabulary(t *testing.T) {
	for _, figure := range []string{"7 days", "$7", "7 percent", "7 devices"} {
		text := "The value is at most (" + figure + ")."
		if got := Extract(text); len(got) != 0 {
			t.Errorf("punctuated prefix %q: %+v", text, got)
		}
	}

	for _, text := range []string{"The wait is at\nleast 7 days.", "The wait is at least 7\ndays."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Qualifier != "≥" || string([]rune(text)[got[0].Start:got[0].End]) != got[0].Text || got[0].Clause != text {
			t.Errorf("wrapped bound and source span %q: %+v", text, got)
		}
	}
	if got := Extract("The wait is at least\n7 days."); len(got) != 1 || got[0].Qualifier != "≥" {
		t.Errorf("wrapped bound: %+v", got)
	}
	for _, figure := range []string{"3 days", "$3", "3 percent", "3 devices"} {
		for _, bound := range []struct{ phrase, want string }{{"greater than", ">"}, {"more than or equal to", "≥"}, {"fewer than or equal to", "≤"}} {
			text := "The value is " + bound.phrase + " " + figure + " and the fee is $5 per 2 devices."
			got := Extract(text)
			if len(got) != 3 || got[0].Text != figure || got[0].Qualifier != bound.want || got[1].Text != "$5" || got[2].Text != "2 devices" {
				t.Errorf("bound %q: %+v", text, got)
			}
		}
	}
	for _, tt := range []struct{ text, qualifier string }{{"The waiting period is greater than 3 days.", ">"}, {"The wait is more than or equal to 3 days.", "≥"}} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Text != "3 days" || got[0].Qualifier != tt.qualifier {
			t.Errorf("standalone bound %q: %+v", tt.text, got)
		}
	}
	for _, text := range []string{"The wait is greater than 3 days and 2 hours.", "The wait is more than or equal to 3 days and 2 hours."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("coordinated bound %q: %+v", text, got)
		}
	}

	got := Extract("The wait is less than 7 days.")
	if len(got) != 1 || got[0].Text != "7 days" || got[0].Qualifier != "<" {
		t.Fatalf("bound: %+v", got)
	}

	t.Run("punctuated bounds", func(t *testing.T) {
		for _, text := range []string{"The wait is 7 days (up to 10 days).", "The wait is 7 days (maximum 10 days)."} {
			got := Extract(text)
			if len(got) != 1 || got[0].Text != "10 days" || got[0].Qualifier != "≤" {
				t.Errorf("%q: %+v", text, got)
			}
		}
		for _, figure := range []string{"10 days", "$10", "10 percent", "10 devices"} {
			for _, bound := range []string{"up to", "maximum", "≤"} {
				text := "The value is (" + bound + " " + figure + ") and the fee is $３ per 2 devices."
				got := Extract(text)
				if len(got) != 3 || got[0].Text != figure || got[0].Qualifier != "≤" || got[1].Text != "$３" || got[2].Text != "2 devices" {
					t.Errorf("%q: %+v", text, got)
					continue
				}
				for _, f := range got {
					if string([]rune(text)[f.Start:f.End]) != f.Text {
						t.Errorf("source offset: %+v", f)
					}
				}
			}
		}
	})

	for _, tt := range []struct{ text, qualifier string }{{"Wait is at  least 7 days.", "≥"}, {"Wait is up\tto 7 days.", "≤"}} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Qualifier != tt.qualifier || got[0].Period.From != "" {
			t.Errorf("phrase whitespace %q: %+v", tt.text, got)
		}
	}
	for _, text := range []string{`The wait is at most "7 days".`, "The wait is at most “7 days”."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Qualifier != "≤" || got[0].Text != "7 days" || string([]rune(text)[got[0].Start:got[0].End]) != "7 days" {
			t.Errorf("%q: %+v", text, got)
		}
	}
	if got := Extract("The wait is a minimum of 7 days."); len(got) != 1 || got[0].Text != "7 days" || got[0].Qualifier != "≥" {
		t.Errorf("minimum: %+v", got)
	}
	if got := Extract("The wait is maximum of 10 days."); len(got) != 1 || got[0].Text != "10 days" || got[0].Qualifier != "≤" {
		t.Errorf("maximum: %+v", got)
	}
	for _, figure := range []string{"7 days", "$７", "7 percent", "7 devices", "7-10 days"} {
		for _, bound := range []struct{ phrase, want string }{{"a minimum of", "≥"}, {"maximum of", "≤"}, {"A MINIMUM  OF", "≥"}} {
			text := "The value is " + bound.phrase + " " + figure + " and the fee is $3 per 2 devices."
			got := Extract(text)
			if len(got) != 3 || got[0].Text != figure || got[0].Qualifier != bound.want || got[1].Text != "$3" || got[2].Text != "2 devices" {
				t.Errorf("%q: %+v", text, got)
				continue
			}
			for _, f := range got {
				if string([]rune(text)[f.Start:f.End]) != f.Text {
					t.Errorf("bad span: %+v", f)
				}
			}
		}
	}
	for _, figure := range []string{"7 days", "$７", "7 percent", "7 devices", "7-10 days"} {
		for _, bound := range []struct{ phrase, want string }{{"less than or equal to", "≤"}, {"greater than or equal to", "≥"}, {"LESS  THAN OR EQUAL TO", "≤"}} {
			text := "The value is " + bound.phrase + " " + figure + " and the fee is $3 per 2 devices."
			got := Extract(text)
			if len(got) != 3 || got[0].Text != figure || got[0].Qualifier != bound.want || got[1].Text != "$3" || got[2].Text != "2 devices" {
				t.Errorf("%q: %+v", text, got)
				continue
			}
			for _, f := range got {
				if string([]rune(text)[f.Start:f.End]) != f.Text {
					t.Errorf("bad span: %+v", f)
				}
			}
		}
	}
	for _, figure := range []string{"$５", "7 days", "7 percent", "7 devices"} {
		for _, bound := range []string{"at most:", "AT  MOST :"} {
			text := "The value is " + bound + " " + figure + " and the fee is $3 per 2 devices."
			got := Extract(text)
			if len(got) != 3 || got[0].Text != figure || got[0].Qualifier != "≤" || got[1].Text != "$3" || got[2].Text != "2 devices" {
				t.Errorf("%q: %+v", text, got)
				continue
			}
			for _, f := range got {
				if string([]rune(text)[f.Start:f.End]) != f.Text {
					t.Errorf("bad span: %+v", f)
				}
			}
		}
	}
	for _, group := range []struct {
		words []string
		want  string
	}{
		{[]string{"up to", "at most", "no more than", "maximum", "max"}, "≤"},
		{[]string{"at least", "minimum", "no less than"}, "≥"},
		{[]string{"more than", "over", "exceeding"}, ">"},
		{[]string{"less than", "under", "fewer than"}, "<"},
		{[]string{"within"}, "within"},
	} {
		for _, w := range group.words {
			g := Extract("Wait is " + w + " 7 days.")
			if len(g) != 1 || g[0].Qualifier != group.want {
				t.Errorf("%q: %+v", w, g)
			}
		}
	}
	for _, tc := range []struct{ text, qualifier string }{
		{"The fee is >= $7.", "≥"}, {"The fee is <= $7.", "≤"},
		{"The wait is >= 7 days.", "≥"}, {"The rate is <= 7 percent.", "≤"}, {"There are >= 7 devices.", "≥"},
	} {
		got := Extract(tc.text)
		if len(got) != 1 || got[0].Qualifier != tc.qualifier {
			t.Errorf("%q: %+v", tc.text, got)
		}
	}
	for _, q := range []string{"≤", "≥", ">", "<"} {
		g := Extract("Wait is " + q + " 7 days.")
		if len(g) != 1 || g[0].Qualifier != q {
			t.Errorf("%s: %+v", q, g)
		}
	}
}
func TestExtractRejectsConflictingPeriodBounds(t *testing.T) {
	for _, clause := range []string{
		"From 1 July 2024 the fee is $7 and from 1 July 2026 the fee is $8.",
		"The fee is $7 until 1 July 2024 and the fee is $8 until 1 July 2026.",
	} {
		text := "The wait is 2 days. " + clause + " The fee is $9."
		got := Extract(text)
		if len(got) != 2 || got[0].Text != "2 days" || got[1].Text != "$9" {
			t.Fatalf("conflicting period bounds retained: %+v", got)
		}
	}
	got := Extract("From 1 July 2026 the fee is $7 and from 1 July 2026 it is $8.")
	if len(got) != 2 || got[0].Period.From != "2026-07-01" || got[1].Period.From != "2026-07-01" {
		t.Fatalf("repeated equal period bounds: %+v", got)
	}
}

func TestExtractMoneyKeepsIndependentlyTypedConjunctions(t *testing.T) {
	for _, amount := range []string{"$7", "USD 7", "7 dollars", "7 USD", "7$"} {
		for _, other := range []string{"2 devices", "two free relocations", "3 hours", "2 percent", "2 dollars"} {
			t.Run(amount+" and "+other, func(t *testing.T) {
				got := Extract("The fee is " + amount + " and " + other + " are included.")
				if len(got) != 2 || got[0].Kind != "money" || got[0].Value != "7" || got[0].Text != amount || got[1].Text != other {
					t.Fatalf("independent typed figure treated as monetary continuation: %+v", got)
				}
			})
		}
	}
}

func TestExtractIdentifierPrecedesEmbeddedMoney(t *testing.T) {
	if g := Extract("Policy: 2026-07-01 applies."); len(g) != 0 {
		t.Fatalf("ambiguous date and identifier: %+v", g)
	}

	for _, tt := range []struct{ text, kind, figure string }{
		{"The invoice is 1000 dollars.", "money", "1000 dollars"},
		{"The order is 1000 devices.", "count", "1000 devices"},
		{"The invoice number is 1000 dollars.", "identifier", "invoice number is 1000"},
		{"The tax ID is 1000 dollars.", "identifier", "tax ID is 1000"},
	} {
		if got := Extract(tt.text); len(got) != 1 || got[0].Kind != tt.kind || got[0].Text != tt.figure {
			t.Errorf("label or amount %q: %+v", tt.text, got)
		}
	}
	for _, identifier := range []string{"USD1234A", "EUR1234", "GBP1234B"} {
		text := "Policy: " + identifier + "."
		got := Extract(text)
		if len(got) != 1 || got[0].Kind != "identifier" || got[0].Value != strings.ToLower(identifier) || got[0].Text != strings.TrimSuffix(text, ".") || got[0].Clause != text {
			t.Fatalf("identifier lost to embedded money in %q: %+v", text, got)
		}
	}
}

func TestExtractMoneySupportsEveryNumberWord(t *testing.T) {
	for _, tt := range []struct{ word, value string }{
		{"zero", "0"}, {"one", "1"}, {"two", "2"}, {"three", "3"},
		{"four", "4"}, {"five", "5"}, {"six", "6"}, {"seven", "7"},
		{"eight", "8"}, {"nine", "9"}, {"ten", "10"}, {"eleven", "11"},
		{"twelve", "12"}, {"thirteen", "13"}, {"fourteen", "14"}, {"fifteen", "15"},
		{"sixteen", "16"}, {"seventeen", "17"}, {"eighteen", "18"}, {"nineteen", "19"}, {"twenty", "20"},
	} {
		for _, prefix := range []string{"$", "USD "} {
			t.Run(prefix+tt.word, func(t *testing.T) {
				got := Extract("The fee is " + prefix + tt.word + ".")
				if len(got) != 1 || got[0].Kind != "money" || got[0].Value != tt.value || got[0].Text != prefix+tt.word {
					t.Fatalf("supported money number word: %+v", got)
				}
			})
		}
	}
}

func TestExtractSkipsOversizedPassage(t *testing.T) {
	s := strings.Repeat("a", MaxPassageBytes) + " 7 days."
	if got := Extract(s); len(got) != 0 {
		t.Fatalf("oversized passage: %+v", got)
	}
}

func TestExtractNumericBoundariesAndCurrencyCodes(t *testing.T) {
	for _, text := range []string{"The fee is CAD $5.", "The fee is USD$10.", "The fee is CAD $5 per 2 devices.", "The fee is $5 CAD.", "The fee is 5 CAD$.", "The fee is $5 and USD $10 per 2 devices."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, s := range []string{"Cost is USD 7.", "Cost is 7 USD."} {
		g := Extract(s)
		if len(g) != 1 || g[0].Unit != "usd" {
			t.Errorf("%s: %+v", s, g)
		}
	}
	for _, s := range []string{"Cost is 1,00 dollars.", "Wait is 1,00 days.", "Wait is " + strings.Repeat("9", 129) + " days.", "The wait is " + strings.Repeat("9", 129) + " days and 3 hours.", "The wait is 1,00 days and 3 hours."} {
		if g := Extract(s); len(g) != 0 {
			t.Errorf("%s: %+v", s, g)
		}
	}
}

func TestExtractRejectsCompoundWordNumerals(t *testing.T) {
	for _, text := range []string{
		"Wait is twenty-one days.",
		"Wait is twenty one days.",
		"Wait is thirty-one days.",
		"Wait is thirty one days.",
		"Wait is one hundred one days.",
		"Wait is one hundred and one days.",
		"Wait is twenty-five days.",
		"Wait is twenty–one days.",
		"Cost is $twenty-one.",
		"Cost is $twenty one.",
		"Cost is $one hundred.",
		"Cost is $twentyone.",
		"Cost is twenty-one dollars.",
		"Rate is twenty-one percent.",
		"There are twenty-one devices.",
	} {
		t.Run(text, func(t *testing.T) {
			if got := Extract(text); len(got) != 0 {
				t.Fatalf("unsupported compound numeral: %+v", got)
			}
		})
	}
}

func TestExtractRejectsIdentifierContinuations(t *testing.T) {
	for _, text := range []string{"Account: 1234 ending in 1234.", "Account: 1234 (ending in 1234).", "Policy: USD1234 (ending in 1234).", "Policy: 2026-07-01 (ending in 0701)."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("masked identifier %q: %+v", text, got)
		}
	}
	for _, tt := range []struct{ text, figure, value string }{
		{"Account: 1234 applies.", "Account: 1234", "1234"},
		{"Policy: USD1234 (active).", "Policy: USD1234", "usd1234"},
	} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Kind != "identifier" || got[0].Text != tt.figure || got[0].Value != tt.value || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.figure {
			t.Errorf("full identifier %q: %+v", tt.text, got)
		}
	}
	for _, text := range []string{
		"Policy: AB1234/56 applies.",
		"Policy: AB1234_CD applies.",
		"Policy: AB1234.CD applies.",
		"Policy: AB1234+56 applies.",
		"Policy: AB1234é applies.",
	} {
		t.Run(text, func(t *testing.T) {
			if got := Extract(text); len(got) != 0 {
				t.Fatalf("truncated identifier: %+v", got)
			}
		})
	}
	for _, text := range []string{"Policy: AB1234.", "Policy: AB1234, applies."} {
		if got := Extract(text); len(got) != 1 || got[0].Value != "ab1234" {
			t.Fatalf("identifier punctuation %q: %+v", text, got)
		}
	}
	{
		for _, token := range []string{"USD1234*", "USD1234_CD", "EUR1234/56", "GBP1234.CD", "USD1234 5678", "AB1234/USD7"} {
			t.Run(token, func(t *testing.T) {
				if got := Extract("Policy: " + token + "."); len(got) != 0 {
					t.Fatalf("rejected identifier became money: %+v", got)
				}
			})
		}
	}
	{
		for _, token := range []string{"XXXX-2026-07-01", "AB-2026-07-01", "2026-07-01/AB", "AB1234/2026-07-01"} {
			if got := Extract("Policy: " + token + " applies."); len(got) != 0 {
				t.Fatalf("identifier %q became another figure: %+v", token, got)
			}
		}
	}
	t.Run("RejectsWhitespaceIdentifierGroups", func(t *testing.T) {
		for _, text := range []string{
			"Account number: 1234 5678.", "Account number: 1234\t5678.",
			"Policy: AB1234 CD5678.", "Account number: １２３４\u202f５６７８.",
		} {
			t.Run(text, func(t *testing.T) {
				if got := Extract(text); len(got) != 0 {
					t.Fatalf("truncated grouped identifier: %+v", got)
				}
			})
		}
		{
			if got := Extract("Policy: AB1234 2026-07-01 applies."); len(got) != 0 {
				t.Fatalf("%+v", got)
			}
		}
	})
}

func TestExtractRejectsSignedNumericPrefixes(t *testing.T) {
	t.Run("spoken signs", func(t *testing.T) {
		for _, text := range []string{"Rate is minus 7 percent.", "Rate is negative 7 percent.", "Rate is minus7 percent.", "Rate is negative7 percent.", "Rate is minus at most 7 percent.", "Fee is negative $7.", "Wait is minus 7-10 days."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
		for _, tt := range []struct {
			text    string
			figures []string
		}{
			{"The negative outlook has a fee of $7.", []string{"$7"}},
			{"Policy: NEGATIVE1234 applies.", []string{"Policy: NEGATIVE1234"}},
			{"The wait is 7 days and the fee is $3.", []string{"7 days", "$3"}},
		} {
			got := Extract(tt.text)
			if len(got) != len(tt.figures) {
				t.Errorf("independent fact %q: %+v", tt.text, got)
				continue
			}
			for i, want := range tt.figures {
				if got[i].Text != want {
					t.Errorf("independent fact %q: %+v", tt.text, got)
				}
			}
		}
	})
	t.Run("accounting", func(t *testing.T) {
		for _, text := range []string{"The adjustment is ($7).", "The adjustment is (($7-10))."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
	})

	for _, text := range []string{
		"Wait is -7 days.",
		"-7 days is unsupported.",
		"Wait is −7 days.",
		"Wait is - 7 days.",
		"Wait is +7 days.",
		"Wait is −7–10 days.",
		"Cost is -$7.",
		"Cost is −7 dollars.",
		"Rate is −7 percent.",
		"There are −7 devices.",
	} {
		t.Run(text, func(t *testing.T) {
			if got := Extract(text); len(got) != 0 {
				t.Fatalf("unsupported signed figure: %+v", got)
			}
		})
	}
}

func TestExtractPreservesAscendingNumericRanges(t *testing.T) {
	t.Run("reversed ranges", func(t *testing.T) {
		for _, text := range []string{
			"Wait is 10-7 days.",
			"The fee is $10-7 and 3 devices.",
			"The wait is 10-7 days and $5.",
			"The wait is 10-7 days and 3 hours.",
			"Wait is 10–7 days.",
			"Wait is 10 to 7 days.",
			"Wait is between 10 and 7 days.",
			"Wait is ten to seven days.",
			"Cost is $10-7.",
			"Cost is 10-7 dollars.",
			"Rate is 10-7 percent.",
			"There are 10-7 devices.",
			"Wait is 10.5-10.25 days.",
		} {
			t.Run(text, func(t *testing.T) {
				if got := Extract(text); len(got) != 0 {
					t.Fatalf("reversed range: %+v", got)
				}
			})
		}
	})
	for _, tt := range []struct{ text, kind, unit, value, end string }{
		{"Wait is 7-10 days.", "duration", "minutes", "10080", "14400"},
		{"Wait is one-two days.", "duration", "minutes", "1440", "2880"},
		{"Wait is 7-ten days.", "duration", "minutes", "10080", "14400"},
		{"Wait is seven-10 days.", "duration", "minutes", "10080", "14400"},
		{"Cost is $seven-ten.", "money", "$", "7", "10"},
		{"Rate is seven-ten percent.", "percent", "percent", "7", "10"},
		{"There are seven-ten devices.", "count", "device", "7", "10"},
		{"Wait is 7–10 days.", "duration", "minutes", "10080", "14400"},
		{"Wait is 7 to 10 days.", "duration", "minutes", "10080", "14400"},
		{"Wait is seven to ten days.", "duration", "minutes", "10080", "14400"},
		{"Wait is between seven and ten days.", "duration", "minutes", "10080", "14400"},
		{"Wait is between 7 and 10 days.", "duration", "minutes", "10080", "14400"},
		{"Wait is 7-7 days.", "duration", "minutes", "10080", "10080"},
		{"Wait is 10.25-10.5 days.", "duration", "minutes", "14760", "15120"},
		{"Cost is $7-10.", "money", "$", "7", "10"},
		{"Rate is 7-10 percent.", "percent", "percent", "7", "10"},
		{"There are 7-10 devices.", "count", "device", "7", "10"},
	} {
		t.Run(tt.text, func(t *testing.T) {
			got := Extract(tt.text)
			if len(got) != 1 || got[0].Kind != tt.kind || got[0].Unit != tt.unit || got[0].Value != tt.value || got[0].EndValue != tt.end || got[0].Qualifier != "" {
				t.Fatalf("ascending range: %+v", got)
			}
		})
	}
	{
		for _, tt := range []struct {
			text, figure, value, end string
			start, finish            int
		}{
			{"Wait is seven–ten days.", "seven–ten days", "10080", "14400", 8, 22},
			{"Wait is seven-ten days.", "seven-ten days", "10080", "14400", 8, 22},
			{"The cooling period is seven-ten days.", "seven-ten days", "10080", "14400", 22, 36},
			{"Wait is 7–ten days.", "7–ten days", "10080", "14400", 8, 18},
			{"Wait is seven–10 days.", "seven–10 days", "10080", "14400", 8, 21},
		} {
			t.Run(tt.text, func(t *testing.T) {
				got := Extract(tt.text)
				if len(got) != 1 || got[0].Kind != "duration" || got[0].Unit != "minutes" || got[0].Qualifier != "" || got[0].Value != tt.value || got[0].EndValue != tt.end || got[0].Text != tt.figure || got[0].Start != tt.start || got[0].End != tt.finish || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.figure {
					t.Fatalf("range: %+v", got)
				}
			})
		}
		text := "The fee is $5/seven–ten devices."
		got := Extract(text)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "seven–ten devices" || got[1].Value != "7" || got[1].EndValue != "10" || got[1].Start != 14 || got[1].End != 31 || string([]rune(text)[got[1].Start:got[1].End]) != got[1].Text {
			t.Fatalf("rate denominator: %+v", got)
		}
	}
}

func TestExtractOmitsCompoundMoneyClauses(t *testing.T) {
	for _, text := range []string{"The fee is $1,00 and membership includes 3 devices.", "The fee is USD7€.", "The fee is €USD7.", "The fee is 7USD€."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, amount := range []string{
		"$1,00 and membership includes 3 devices", "USD7€", "$7 and 50 cents", "$7 and fifty cents", "USD 7 and fifty cents",
		"7 dollars and 50 cents", "seven dollars and fifty cents", "7 USD and fifty cents",
		"7$ and fifty cents", "7€ and fifty cents", "7 euros and fifty cents",
		"$seven fifty", "7 dollars fifty cents", "$7 50 cents",
		"$7 AND\tfifty cents", "＄７ and fifty cents",
		"$7 and a half", "7 dollars and a half", "USD 7 and a half",
		"seven dollars, fifty cents", "$7, plus 50 cents", "$7 plus 50 cents", "$7 + 50 cents",
	} {
		t.Run(amount, func(t *testing.T) {
			// Dropping only the first amount must not leave another figure in
			// the unsupported clause available for comparison.
			text := "Wait is ２ days. Cost is " + amount + " and $8 for 3 devices in 4 hours; The fee is １２ dollars."
			got := Extract(text)
			if len(got) != 2 || got[0].Text != "２ days" || got[0].Value != "2880" || got[1].Text != "１２ dollars" || got[1].Value != "12" {
				t.Fatalf("unsupported money clause left partial figures: %+v", got)
			}
			runes := []rune(text)
			for _, f := range got {
				if string(runes[f.Start:f.End]) != f.Text || string(runes[f.ClauseStart:f.ClauseEnd]) != f.Clause {
					t.Fatalf("surrounding original offsets changed: %+v", f)
				}
			}
		})
	}
}

func TestExtractCompoundDurationKeepsComponents(t *testing.T) {
	text := "The service cooling period is 7 days and 3 hours."
	got := Extract(text)
	if len(got) != 2 || got[0].Text != "7 days" || got[0].Value != "10080" || got[1].Text != "3 hours" || got[1].Value != "180" {
		t.Fatalf("compound duration lost components: %+v", got)
	}
	for _, f := range got {
		if f.Kind != "duration" || f.Unit != "minutes" || string([]rune(text)[f.Start:f.End]) != f.Text || f.Clause != text {
			t.Fatalf("duration components changed kind, unit or source: %+v", f)
		}
	}
}

func TestExtractDateImmediatelyBeforeFigure(t *testing.T) {
	t.Run("masked date", func(t *testing.T) {
		for _, tt := range []struct{ text, want string }{
			{"USD July 2026 7 days.", "7 days"}, {"USD July 2026 7 devices.", "7 devices"},
		} {
			got := Extract(tt.text)
			if len(got) != 1 || got[0].Text != tt.want || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.want {
				t.Errorf("%q: %+v", tt.text, got)
			}
		}
	})

	text := "From 1 July 2026 15 days are allowed."
	got := Extract(text)
	if len(got) != 1 {
		t.Fatalf("dated figure: %+v", got)
	}
	f := got[0]
	if f.Kind != "duration" || f.Value != "21600" || f.Unit != "minutes" || f.Text != "15 days" || f.Start != 17 || f.End != 24 || f.Clause != text || f.ClauseStart != 0 || f.ClauseEnd != 37 || f.Period != (Period{From: "2026-07-01"}) {
		t.Fatalf("dated figure lost source or period provenance: %+v", f)
	}
}

func TestExtractSlashRates(t *testing.T) {
	for _, tt := range []struct {
		text    string
		figures []string
	}{
		{"The fee is $5/month.", []string{"$5"}},
		{"The fee is $5/2 devices.", []string{"$5", "2 devices"}},
		{"The fee is $5 / 2-3 free devices.", []string{"$5", "2-3 free devices"}},
		{"The fee is $5 per 2 devices.", []string{"$5", "2 devices"}},
		{"The fee is $5 each month.", []string{"$5"}},
		{"The fee is $5 a month.", []string{"$5"}},
	} {
		t.Run(tt.text, func(t *testing.T) {
			got := Extract(tt.text)
			if len(got) != len(tt.figures) {
				t.Fatalf("got %+v, want %v", got, tt.figures)
			}
			for i, f := range got {
				if f.Text != tt.figures[i] || string([]rune(tt.text)[f.Start:f.End]) != f.Text || f.Clause != tt.text {
					t.Fatalf("figure lost source: %+v", f)
				}
			}
		})
	}
	{
		for _, tt := range []struct {
			text    string
			figures []struct {
				text, kind string
				start, end int
			}
		}{
			{"Rate is 2 devices/5 days.", []struct {
				text, kind string
				start, end int
			}{{"2 devices", "count", 8, 17}, {"5 days", "duration", 18, 24}}},
			{"Rate is 5 days/2 devices.", []struct {
				text, kind string
				start, end int
			}{{"5 days", "duration", 8, 14}, {"2 devices", "count", 15, 24}}},
			{"Rate is 5 percent/2 devices.", []struct {
				text, kind string
				start, end int
			}{{"5 percent", "percent", 8, 17}, {"2 devices", "count", 18, 27}}},
			{"Rate is 2 devices/5 users.", []struct {
				text, kind string
				start, end int
			}{{"2 devices", "count", 8, 17}, {"5 users", "count", 18, 25}}},
		} {
			t.Run(tt.text, func(t *testing.T) {
				got := Extract(tt.text)
				if len(got) != len(tt.figures) {
					t.Fatalf("rate: %+v", got)
				}
				for i, want := range tt.figures {
					f := got[i]
					if f.Text != want.text || f.Kind != want.kind || f.Start != want.start || f.End != want.end || f.Clause != tt.text || string([]rune(tt.text)[f.Start:f.End]) != want.text {
						t.Fatalf("figure: %+v", f)
					}
				}
			})
		}
	}
}

func TestExtractRejectsNumericGrouping(t *testing.T) {
	for _, text := range []string{
		"Wait is 1 000 days.", "Cost is 1 000 dollars.", "Cost is $1 000.",
		"Rate is 1\u202f000 percent.", "There are 1\t000 devices.",
	} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("grouped %q: %+v", text, got)
		}
	}

	for _, separator := range []string{"_", "'", "’", "‘", "ʼ"} {
		for _, text := range []string{"Cost is $1" + separator + "000.", "Cost is CHF 1" + separator + "000.", "Cost is 1" + separator + "000 dollars."} {
			if got := Extract(text); len(got) != 0 {
				t.Errorf("grouped %q: %+v", text, got)
			}
		}
	}
	for _, text := range []string{"Cost is '$7'.", "Cost is ‘$7’.", `Cost is "$7".`} {
		if got := Extract(text); len(got) != 1 || got[0].Text != "$7" {
			t.Errorf("quoted %q: %+v", text, got)
		}
	}
	t.Run("RejectsCommaSpaceGrouping", func(t *testing.T) {
		if got := Extract("The fee is $1, 500.50 dollars."); len(got) != 0 {
			t.Errorf("decimal grouping: %+v", got)
		}
		if got := Extract("The fee is $1, 500 dollars."); len(got) != 0 {
			t.Errorf("%+v", got)
		}
		if got := Extract("The fee is 1, 500 dollars."); len(got) != 0 {
			t.Fatalf("%+v", got)
		}
		for _, tt := range []struct {
			text    string
			figures []string
		}{
			{"Fee is $1, 500 devices.", []string{"$1", "500 devices"}},
			{"The fees are $1, $500.", []string{"$1", "$500"}},
			{"On 1 July 2026, 500 devices apply.", []string{"500 devices"}},
		} {
			got := Extract(tt.text)
			if len(got) != len(tt.figures) {
				t.Errorf("independent figure %q: %+v", tt.text, got)
				continue
			}
			for i, want := range tt.figures {
				if got[i].Text != want {
					t.Errorf("independent figure %q: %+v", tt.text, got)
				}
			}
		}
	})
}

func TestExtractSentenceBoundaryAfterClosingPunctuation(t *testing.T) {
	for _, tt := range []struct{ text, want string }{{"“The date is 1 July 2026.”", "1 July 2026"}, {"“Policy: ABC1234.”", "Policy: ABC1234"}} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Text != tt.want || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.want {
			t.Errorf("%q: %+v", tt.text, got)
		}
	}
	for _, tt := range []struct {
		text    string
		figures []struct {
			quote, clause string
			period        Period
		}
	}{
		{`"The fee is $7." From 1 July 2026 the wait is 2 days.`, []struct {
			quote, clause string
			period        Period
		}{{"$7", `"The fee is $7."`, Period{}}, {"2 days", "From 1 July 2026 the wait is 2 days.", Period{From: "2026-07-01"}}}},
		{`(No fee applies.) The wait is 2 days.`, []struct {
			quote, clause string
			period        Period
		}{{"2 days", "The wait is 2 days.", Period{}}}},
	} {
		got := Extract(tt.text)
		if len(got) != len(tt.figures) {
			t.Errorf("%q: %+v", tt.text, got)
			continue
		}
		for i, want := range tt.figures {
			f := got[i]
			if f.Text != want.quote || f.Clause != want.clause || f.Period != want.period || string([]rune(tt.text)[f.Start:f.End]) != want.quote || string([]rune(tt.text)[f.ClauseStart:f.ClauseEnd]) != want.clause {
				t.Errorf("%q: %+v", tt.text, f)
			}
		}
	}
}

func TestExtractIndependentPeriodScopes(t *testing.T) {
	got := Extract("The wait is 7 days with less effort and the fee is $5 per 2 devices.")
	if len(got) != 3 || got[0].Text != "7 days" || got[1].Text != "$5" || got[2].Text != "2 devices" {
		t.Fatalf("independent figures: %+v", got)
	}
	for _, tt := range []struct{ text, end string }{
		{"The fee is $7 until 1 July 2026.", "2026-07-01"},
		{"The fee is $7 to 1 July 2026.", "2026-07-01"},
		{"The fee is $7 to 30 June 2026.", "2026-06-30"},
	} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Text != "$7" || got[0].Period.To != tt.end {
			t.Errorf("%q: %+v", tt.text, got)
		}
	}

	for _, s := range []string{"The notice is due (2 days) before 1 July 2026.", "The notice is due \"2 days\" prior to 1 July 2026.", "The notice period is 2 days before 1 July 2026.", "From 1 January 2026 the notice is 2 days before 1 July 2026.", "The notice is due 2 days prior to 1 July 2026.", "From 1 January 2026 the notice is due 2 days prior to 1 July 2026."} {
		got := Extract(s)
		if len(got) != 1 || got[0].Text != "2 days" || got[0].Period != (Period{}) {
			t.Errorf("%q: %+v", s, got)
			continue
		}
		if string([]rune(s)[got[0].Start:got[0].End]) != "2 days" {
			t.Errorf("bad span: %+v", got[0])
		}
	}

	text := "The wait is 7 days and 3 hours and 5 minutes from 1 July 2026."
	got = Extract(text)
	if len(got) != 3 {
		t.Fatalf("components: %+v", got)
	}
	for i, want := range []string{"7 days", "3 hours", "5 minutes"} {
		if got[i].Text != want || got[i].Period.From != "2026-07-01" || string([]rune(text)[got[i].Start:got[i].End]) != want {
			t.Errorf("component %d: %+v", i, got[i])
		}
	}

	for _, text := range []string{"The wait is about 2 days from 1 July 2026 and the fee is $7.", "The fee is $7 and the wait is about 2 days from 1 July 2026."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Text != "$7" || got[0].Period != (Period{}) {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, text := range []string{"The fee is $7 and notice is about 2 days before 1 July 2026.", "From 1 January 2026 the fee is $7 and notice is about 2 days before 1 July 2026."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Text != "$7" || got[0].Period != (Period{}) {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, tt := range []struct {
		text    string
		figures []string
	}{
		{"From 1 July 2026 the wait is about 2 days and the fee is $7.", []string{"$7"}},
		{"From 1 July 2026 the fee is $5 per 2 devices and the wait is about 3 days.", []string{"$5", "2 devices"}},
	} {
		got := Extract(tt.text)
		if len(got) != len(tt.figures) {
			t.Errorf("shared prefix %q: %+v", tt.text, got)
			continue
		}
		for i, want := range tt.figures {
			if got[i].Text != want || got[i].Period != (Period{From: "2026-07-01"}) {
				t.Errorf("shared prefix %q: %+v", tt.text, got)
			}
		}
	}
	text = "The fee is $7 from 1 July 2026 and the wait is 2 days until 30 June 2027."
	got = Extract(text)
	if len(got) != 2 || got[0].Text != "$7" || got[1].Text != "2 days" || got[0].Period != (Period{}) || got[1].Period != (Period{}) {
		t.Fatalf("%+v", got)
	}
	got = Extract("The fee is $7 from 1 July 2026 and the wait is 2 days.")
	if len(got) != 2 || got[0].Period != (Period{}) || got[1].Period != (Period{}) {
		t.Fatalf("single scoped bound: %+v", got)
	}
	got = Extract("The fee is $7 and the wait is 2 days until 30 June 2027.")
	if len(got) != 2 || got[0].Period != (Period{}) || got[1].Period != (Period{}) {
		t.Fatalf("trailing scoped bound: %+v", got)
	}
	for _, text := range []string{"From 1 July 2026 the fee is $5/2 devices.", "The fee is $5/2 devices from 1 July 2026.", "From 1 July 2026 the wait is 7 days and 3 hours.", "The wait is 7 days and 3 hours from 1 July 2026."} {
		got := Extract(text)
		if len(got) != 2 || got[0].Period.From != "2026-07-01" || got[1].Period.From != "2026-07-01" {
			t.Errorf("shared period %q: %+v", text, got)
		}
	}
}

func TestExtractAlternativeDateBounds(t *testing.T) {
	for _, text := range []string{"From 1 July 2026 or 1 August 2026 the fee is $7.", "The fee is $7 from 1 July 2026 or 1 August 2026.", "Until 1 July 2026 or 1 August 2026 the fee is $7.", "The fee is $7 until 1 July 2026 or 1 August 2026."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Text != "$7" || got[0].Period != (Period{}) || string([]rune(text)[got[0].Start:got[0].End]) != "$7" {
			t.Errorf("alternative bounds %q: %+v", text, got)
		}
	}
}

func TestExtractExclusivePeriodEnd(t *testing.T) {
	for _, tt := range []struct{ text, end string }{
		{"The fee is $7 before 1 July 2026.", "2026-06-30"},
		{"The fee is $7 prior to 1 July 2026.", "2026-06-30"},
		{"The fee is $7 through 1 July 2026.", "2026-07-01"},
		{"The fee is $7 before March 2024.", "2024-02-29"},
	} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Period.To != tt.end || string([]rune(tt.text)[got[0].Start:got[0].End]) != "$7" {
			t.Errorf("%q: %+v", tt.text, got)
		}
	}
	for _, text := range []string{"From 1 January 0001 the fee is $7 before January 0001.", "From 1 July 2026 the fee is $7 before 1 July 2026."} {
		got := Extract(text)
		if len(got) != 1 || got[0].Period != (Period{}) {
			t.Errorf("unrepresentable period %q: %+v", text, got)
		}
	}
}

func TestExtractMixedPrecisionPeriodBounds(t *testing.T) {
	for _, tt := range []struct {
		text string
		want Period
	}{
		{"From 1 July 2026 until July 2026 the fee is $5.", Period{From: "2026-07-01", To: "2026-07"}},
		{"From 31 July 2026 until July 2026 the fee is $5.", Period{From: "2026-07-31", To: "2026-07"}},
		{"From 1 August 2026 until July 2026 the fee is $5.", Period{}},
		{"From July 2026 until 1 July 2026 the fee is $5.", Period{From: "2026-07", To: "2026-07-01"}},
	} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Text != "$5" || got[0].Period != tt.want {
			t.Errorf("%q: %+v", tt.text, got)
		}
	}
}

func TestExtractRejectsPostfixBounds(t *testing.T) {
	t.Run("punctuated postfix bounds", func(t *testing.T) {
		for _, figure := range []string{"7 days", "$7", "7 percent", "7 devices"} {
			for _, text := range []string{"The value is (" + figure + ") maximum.", "The value is " + figure + " (maximum).", "The value is " + figure + ", max."} {
				if got := Extract(text); len(got) != 0 {
					t.Errorf("%q: %+v", text, got)
				}
			}
		}
	})

	for _, text := range []string{"The fee is $5 and up.", "The delay is 7 days and above.", "The wait is 7 days and over.", "The fee is $7 and over."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, figure := range []string{"7 days", "$7", "7 percent", "7 devices"} {
		for _, bound := range []string{"maximum", "minimum", "max", "at most", "at  most", "at\tmost", "at least", "no more than", "no less than", "at a maximum", "at the maximum", "at minimum", "at maximum", "(at minimum)", ", at maximum", "a maximum", "the minimum", "and up", "and above", "and over", "and\tover"} {
			text := "Value is " + figure + " " + bound + "."
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
	}
	got := Extract("The wait is 7 days and over. The fee is $3.")
	if len(got) != 1 || got[0].Text != "$3" {
		t.Fatalf("independent sentence after bound: %+v", got)
	}
	got = Extract("The wait is 7 days and the maximum fee is $3.")
	if len(got) != 2 || got[0].Text != "7 days" || got[1].Text != "$3" {
		t.Fatalf("independent bound: %+v", got)
	}
	got = Extract("The wait is 7 days over summer.")
	if len(got) != 1 || got[0].Text != "7 days" {
		t.Fatalf("unrelated preposition: %+v", got)
	}
	t.Run("CompoundDurationBounds", func(t *testing.T) {
		for _, s := range []string{
			"The service period is a minimum of 7 days and 3 hours.",
			"The service period is up to 7 days and 3 hours.",
			"The service period is at least 7 days and 3 months.",
			"The service period is 7 days and 3 hours maximum.",
			"The service period is 7 days at  most, and 3 hours.",
			"The service period is 7 days at\tmost, and 3 hours.",
			"The service period is 7 days maximum, and 3 hours.",
			"The service period is 7 days at a maximum and 3 hours.",
			"The service period is 7 days and 3 hours at the maximum.",
		} {
			if got := Extract(s); len(got) != 0 {
				t.Errorf("%q: %+v", s, got)
			}
		}
		s := "The wait is up to 7 days and 3 hours, and the fee is $5 per 2 devices."
		got := Extract(s)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Fatalf("%+v", got)
		}
		r := []rune(s)
		for _, f := range got {
			if string(r[f.Start:f.End]) != f.Text {
				t.Fatalf("bad span: %+v", f)
			}
		}
		s = "The wait is 7 days maximum, and 3 hours, and the fee is $5 per 2 devices."
		got = Extract(s)
		if len(got) != 2 || got[0].Text != "$5" || got[1].Text != "2 devices" {
			t.Fatalf("%+v", got)
		}
		r = []rune(s)
		for _, f := range got {
			if string(r[f.Start:f.End]) != f.Text {
				t.Fatalf("bad span: %+v", f)
			}
		}
	})
}

func TestExtractCalendarQualifiedDurations(t *testing.T) {
	for _, text := range []string{"The wait is 7 days excluding 3 hours.", "The wait is 7 days except for 3 hours.", "The wait excludes time, excluding the 3 hours."} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("numeric exclusion %q: %+v", text, got)
		}
	}
	for _, day := range []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"} {
		for _, suffix := range []string{"", "s"} {
			text := "Wait is 7 days excluding " + day + suffix + "."
			if got := Extract(text); len(got) != 0 {
				t.Errorf("%q: %+v", text, got)
			}
		}
	}
	for _, text := range []string{
		"The wait is (7 days) excluding weekends.", `The wait is "7 days" excluding weekends.`,
		"The wait is 7 days excluding the weekend.", "The wait is 7 days excluding Christmas.",
		"The wait is 7 days excluding the winter holiday and 3 hours.",
		"The cooling-off period is 7 days excluding weekends.",
		"The cancellation period is 7 days excluding Saturday and Sunday.",
		"The wait is 7 days excluding Sundays.", "The wait is 7 days, except holidays.",
		"The wait is 7 days (excluding Mondays) and 3 hours.",
		"The wait is 7 days and 3 hours, except Fridays.",
		"The wait is 7 days excluding holidays.",
		"The wait is 7 days excluding public holidays.",
		"The wait is 7 days and 3 hours excluding weekends and holidays.",
		"The wait is 7 days excluding weekends and 3 hours.",
	} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"The wait is 2 days excluding Christmas before 1 July 2026 and the fee is $5 per 2 devices.", []string{"$5", "2 devices"}},
		{"The wait is 7 days excluding weekends and the fee is $5 per 2 devices.", []string{"$5", "2 devices"}},
		{"The wait is 2 days excluding holidays before 1 July 2026 and the fee is $5.", []string{"$5"}},
		{"The fee is $5 and notice is 2 days excluding holidays before 1 July 2026.", []string{"$5"}},
		{"The fee is $5 and notice is 2 days, except Sundays, before 1 July 2026.", []string{"$5"}},
	} {
		text := tt.text
		got := Extract(text)
		if len(got) != len(tt.want) {
			t.Errorf("%q: %+v", text, got)
			continue
		}
		for i, f := range got {
			if f.Text != tt.want[i] || f.Period != (Period{}) || string([]rune(text)[f.Start:f.End]) != f.Text {
				t.Errorf("figure and source span: %+v", f)
			}
		}
	}
	for _, tt := range []struct{ text, figure, unit, value string }{
		{"Wait is 7 business days.", "7 business days", "business days", "7"},
		{"Wait is 7 working days.", "7 working days", "business days", "7"},
		{"Wait is 7 calendar days.", "7 calendar days", "minutes", "10080"},
		{"Wait is 7 days including weekends.", "7 days", "minutes", "10080"},
	} {
		got := Extract(tt.text)
		if len(got) != 1 || got[0].Kind != "duration" || got[0].Text != tt.figure || got[0].Unit != tt.unit || got[0].Value != tt.value || string([]rune(tt.text)[got[0].Start:got[0].End]) != tt.figure {
			t.Errorf("%q: %+v", tt.text, got)
		}
	}
	got := Extract("Wait is 7 days excluding weekends. Fee is $5.")
	if len(got) != 1 || got[0].Text != "$5" {
		t.Errorf("independent clause: %+v", got)
	}
}
