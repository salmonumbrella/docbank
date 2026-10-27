package grounding

import (
	"reflect"
	"strings"
	"testing"
)

func TestDateNormalization(t *testing.T) {
	for _, text := range []string{"2026-03-15", "2026/03/15", "15.03.2026", "03/15/2026", "15/03/2026", "March 15, 2026", "15 March 2026", "Mar. 15, 2026", "15 Mar. 2026", "Ｍａｒ． １５, ２０２６"} {
		t.Run(text, func(t *testing.T) {
			r := Check(text+" [1].", []Citation{{Index: 1, Text: "2026-03-15"}})
			if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 {
				t.Fatalf("%+v", r)
			}
			f := r.Claims[0].Figures[0]
			if f.Kind != "date" || !reflect.DeepEqual(f.Values, []string{"2026-03-15"}) || f.Text != text {
				t.Fatalf("%+v", f)
			}
		})
	}
	for _, text := range []string{"March 2026", "Mar. 2026", "2026-03"} {
		f := extract(text)
		if len(f) != 1 || f[0].Kind != "date" || !reflect.DeepEqual(f[0].Values, []string{"2026-03"}) {
			t.Fatalf("%q: %+v", text, f)
		}
	}
	f := extract("03/04/2026")
	if len(f) != 1 || !reflect.DeepEqual(f[0].Values, []string{"2026-04-03", "2026-03-04"}) {
		t.Fatalf("%+v", f)
	}
	for _, tc := range []struct{ answer, source string }{
		{"March 2026", "2026-03-15"}, {"2026-03-15", "March 2026"},
		{"2026-03-15", "INV-2026-03-15"}, {"2026-03-15", "2026-03-150"},
	} {
		if r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}}); r.Status != "ungrounded" {
			t.Fatalf("%+v", r)
		}
	}
}

func TestNonbreakingDateSpacesGroundAnswer(t *testing.T) {
	for _, tc := range []struct{ name, space string }{{"nbsp", "\u00a0"}, {"narrow-nbsp", "\u202f"}} {
		t.Run(tc.name, func(t *testing.T) {
			source := "March" + tc.space + "15," + tc.space + "2026"
			r := Check("March 15, 2026 [1]", []Citation{{Index: 1, Text: source}})
			if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 {
				t.Fatalf("%+v", r)
			}
			figure := r.Claims[0].Figures[0]
			if figure.Kind != "date" || !reflect.DeepEqual(figure.Values, []string{"2026-03-15"}) || !reflect.DeepEqual(figure.FoundIn, []int{1}) {
				t.Fatalf("%+v", figure)
			}
		})
	}
}

func TestISO8601TimestampDateGroundsAnswer(t *testing.T) {
	r := Check("Released 2026-03-15 [1]", []Citation{{Index: 1, Text: "released_at: 2026-03-15T10:00:00Z"}})
	if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 {
		t.Fatalf("%+v", r)
	}
	figure := r.Claims[0].Figures[0]
	if figure.Kind != "date" || figure.Text != "2026-03-15" || !reflect.DeepEqual(figure.Values, []string{"2026-03-15"}) || !reflect.DeepEqual(figure.FoundIn, []int{1}) {
		t.Fatalf("%+v", figure)
	}
}

func TestInvalidDatesFallThroughToNumbers(t *testing.T) {
	for _, text := range []string{"2026-02-30", "31.02.2026", "13/14/2026", "0000-03-15", "2026-03-00", "00.03.2026"} {
		fs := extract(text)
		if len(fs) != 3 {
			t.Fatalf("%q: %+v", text, fs)
		}
		for _, f := range fs {
			if f.Kind != "number" {
				t.Fatalf("%q: %+v", text, fs)
			}
		}
	}
	if fs := extract("2024-02-29"); len(fs) != 1 || fs[0].Kind != "date" {
		t.Fatalf("%+v", fs)
	}
}

func TestAmountNormalization(t *testing.T) {
	for _, tc := range []struct{ answer, source string }{
		{"$1.2M", "$1,200,000"}, {"$1.2 thousand", "USD 1200"}, {"40 USD", "$40"}, {"USD 40", "$40"},
		{"US$40", "USD 40"}, {"C$40", "CAD 40"}, {"A$40", "AUD 40"}, {"$40", "NZD 40"},
		{"¥40", "JPY 40"}, {"¥40", "CNY 40"}, {"£40", "GBP 40"}, {"€40", "EUR 40"},
		{"₹40", "₹40"}, {"40€", "EUR 40"}, {"-$20", "USD -20"}, {"$-20", "-20 USD"},
		{"-USD 20", "USD -20"}, {"USD 1.2bn", "USD 1200000000"}, {"€0.000000000000000001m", "€0.000000000001"},
		{"USD 12345678901234567890.123456789", "$12345678901234567890.123456789"},
		{`\$1\.5`, "$1.5"}, {"＄２０", "USD 20"},
	} {
		t.Run(tc.answer+"/"+tc.source, func(t *testing.T) {
			r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
			if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "amount" {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestAmountValueOnlyAndScope(t *testing.T) {
	for _, tc := range []struct{ answer, source, status string }{
		{"€500", "$500", "value_only"}, {"USD 40", "CAD 40", "value_only"}, {"C$40", "A$40", "value_only"},
		{"$40", "40", "value_only"}, {"$40", "EUR 40", "value_only"}, {"$40", "USD 400", "missing"},
		{"-$40", "USD 40", "missing"}, {"USD 40", "XUSD 40", "value_only"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Status != tc.status || len(r.Claims[0].Figures[0].FoundIn) != 0 {
			t.Fatalf("%+v", r)
		}
	}
	r := Check("$40 [1]", []Citation{{Index: 1, Text: "40"}, {Index: 2, Text: "USD 40"}})
	if r.Claims[0].Figures[0].Status != "value_only" {
		t.Fatalf("%+v", r)
	}
	r = Check("$40", []Citation{{Index: 1, Text: "40"}, {Index: 2, Err: "citation_unavailable"}})
	if r.Claims[0].Figures[0].Status != "value_only" {
		t.Fatalf("%+v", r)
	}
	r = Check("$40", []Citation{{Index: 1, Text: "40"}, {Index: 2, Text: "USD 40"}})
	if r.Status != "grounded" || !reflect.DeepEqual(r.Claims[0].Figures[0].FoundIn, []int{2}) {
		t.Fatalf("%+v", r)
	}
}

func TestAmountCanonicalValuesAndOffsets(t *testing.T) {
	text := `猫 \$1\.20M`
	fs := extract(text)
	if len(fs) != 1 || fs[0].Start != 2 || fs[0].End != 10 || fs[0].Text != `\$1\.20M` || !reflect.DeepEqual(fs[0].Values, []string{"$ 1200000"}) {
		t.Fatalf("%+v", fs)
	}
	for _, text := range []string{"INV-USD40", "USD40X", "40USDX", "USD 40-50", "USD 1,2,3"} {
		for _, f := range extract(text) {
			if f.Kind == "amount" {
				t.Fatalf("partial amount in %q: %+v", text, f)
			}
		}
	}
}

func TestMalformedAmountDoesNotExposeNormalizedValue(t *testing.T) {
	for _, text := range []string{"$1,200,,", "$1,200..", "USD 1''200"} {
		r := Check("$1200", []Citation{{Index: 1, Text: text}})
		if r.Status != "ungrounded" {
			t.Fatalf("%q: %+v", text, r)
		}
	}
}
func TestNumberMatchesAmountValue(t *testing.T) {
	r := Check("40", []Citation{{Index: 1, Text: "USD 40"}})
	if r.Status != "grounded" {
		t.Fatalf("%+v", r)
	}
}

func TestConflictingCurrencyMarkersCannotGroundSingleCurrency(t *testing.T) {
	for _, text := range []string{"$10EUR", "USD 10 EUR", "USD $10 EUR", "USD CAD 10"} {
		for _, single := range []string{"USD 10", "EUR 10", "CAD 10"} {
			r := Check(text, []Citation{{Index: 1, Text: single}})
			if r.Status != "ungrounded" {
				t.Fatalf("%q against %q: %+v", text, single, r)
			}
			r = Check(single, []Citation{{Index: 1, Text: text}})
			if r.Status != "ungrounded" {
				t.Fatalf("%q against %q: %+v", single, text, r)
			}
		}
		r := Check(text, []Citation{{Index: 1, Text: text}})
		if r.Status != "grounded" {
			t.Fatalf("self: %+v", r)
		}
	}
}

func TestPositiveAmountSignsRetainCurrency(t *testing.T) {
	for _, text := range []string{"$+20", "USD +20", "+$20", "+USD 20", "20 USD"} {
		for _, source := range []string{"20", "EUR 20"} {
			r := Check(text, []Citation{{Index: 1, Text: source}})
			if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "amount" || r.Claims[0].Figures[0].Status != "value_only" {
				t.Fatalf("%q against %q: %+v", text, source, r)
			}
		}
		if r := Check(text, []Citation{{Index: 1, Text: "USD 20"}}); r.Status != "grounded" {
			t.Fatalf("%+v", r)
		}
	}
}

func TestCompatibleCurrencyMarkersNarrowToExplicitCode(t *testing.T) {
	for _, text := range []string{"USD $10", "$10 USD", "USD 10$", "US$10 USD"} {
		fs := extract(text)
		if len(fs) != 1 || fs[0].Kind != "amount" || !reflect.DeepEqual(fs[0].Values, []string{"USD 10"}) || fs[0].Text != text {
			t.Fatalf("%q: %+v", text, fs)
		}
		if r := Check(text, []Citation{{Index: 1, Text: "CAD 10"}}); r.Status != "ungrounded" {
			t.Fatalf("%+v", r)
		}
	}
}

func TestNonbreakingProseSpacesPreserveAmountCurrency(t *testing.T) {
	for _, space := range []string{" ", "\u00a0", "\u202f"} {
		r := Check("Fee: $20"+space+"total", []Citation{{Index: 1, Text: "20" + space + "total"}})
		if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "amount" || r.Claims[0].Figures[0].Status != "value_only" {
			t.Fatalf("%q: %+v", space, r)
		}
	}
}

func TestCurrencyCodesMustEndBeforeProse(t *testing.T) {
	for _, answer := range []string{"USD 20 allocated", "$20 allocated", "$20 allowance", "USD 20 money"} {
		r := Check(answer, []Citation{{Index: 1, Text: "20"}})
		if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "amount" || r.Claims[0].Figures[0].Status != "value_only" {
			t.Fatalf("%q: %+v", answer, r)
		}
	}
}

func TestCurrencyPrefixFailureAdvances(t *testing.T) {
	for _, tail := range []string{"", "++", "1unknown", "1,2,3"} {
		rs := []rune(strings.Repeat("$", 16000-len(tail)) + tail)
		end, values := amountAt(rs, 0)
		if end < 16000-len(tail) || len(values) != 0 {
			t.Fatalf("failed prefix did not advance: end=%d values=%v", end, values)
		}
	}
	if fs := extract(strings.Repeat("$", 16000)); len(fs) != 0 {
		t.Fatalf("%+v", fs)
	}
}

func TestRepeatedGroupingSeparatorsDoNotExposeNumericSuffix(t *testing.T) {
	for _, separator := range []string{"\u00a0", "\u202f", "'"} {
		source := "1" + separator + separator + "200"
		if r := Check("200", []Citation{{Index: 1, Text: source}}); r.Status != "ungrounded" {
			t.Fatalf("%q: %+v", source, r)
		}
	}
}

func TestCurrencyPrefixDoesNotDiscardIdentifierParts(t *testing.T) {
	for _, text := range []string{"USD-A2", "USD_A2", "USD/A2", "USD.A2", "CAD-A2", "USD-20-A2"} {
		r := Check(text, []Citation{{Index: 1, Text: "A2"}})
		if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "identifier" || r.Claims[0].Figures[0].Text != text {
			t.Fatalf("%q: %+v", text, r)
		}
		if r := Check(text, []Citation{{Index: 1, Text: text}}); r.Status != "grounded" {
			t.Fatalf("self %q: %+v", text, r)
		}
	}
	for _, text := range []string{"USD-20", "USD -20", "USD- 20", "USD+20"} {
		if r := Check(text, []Citation{{Index: 1, Text: text}}); r.Status != "grounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "amount" {
			t.Fatalf("amount %q: %+v", text, r)
		}
	}
}

func TestFailedCurrencyPrefixRecoversAtSafeTokenBoundary(t *testing.T) {
	for _, text := range []string{"USD USD-A2", "$ USD/A2", "-USD_A2", "USD USD/20/A2"} {
		r := Check(text, []Citation{{Index: 1, Text: "A2"}})
		if r.Status != "ungrounded" {
			t.Fatalf("%q: %+v", text, r)
		}
	}
	for _, text := range []string{"-$1,2,3", "USD -1,2,3", "USD USD -1,2,3"} {
		r := Check(text, []Citation{{Index: 1, Text: "1,2,3"}})
		if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 1 {
			t.Fatalf("%q: %+v", text, r)
		}
	}
	rs := []rune(strings.Repeat("USD ", 4000))
	if end, values := amountAt(rs, 0); end != len(rs) || len(values) != 0 {
		t.Fatalf("bare ISO run did not advance: end=%d", end)
	}
}

func TestJoinedCurrencyCodeTokensPreserveRecoveryBoundary(t *testing.T) {
	rs := []rune(strings.Repeat("USD.", 4000))
	if end, values := amountAt(rs, 0); end != 0 || len(values) != 0 {
		t.Fatalf("joined token prefix was discarded: end=%d", end)
	}
	if fs := extract(string(rs)); len(fs) != 0 {
		t.Fatalf("%+v", fs)
	}
	rs = []rune(strings.Repeat("US$", 5333))
	if end, values := amountAt(rs, 0); end != len(rs) || len(values) != 0 {
		t.Fatalf("symbol marker run did not advance: end=%d", end)
	}
}

func TestNormalizedAmountsRespectCitationScope(t *testing.T) {
	citations := []Citation{{Index: 1, Text: "USD 10"}, {Index: 2, Text: "EUR 10"}}
	for _, tc := range []struct{ answer, status, first string }{
		{"USD 10. EUR 10 [2].", "incomplete", "unverifiable"},
		{"USD 10 [2].", "ungrounded", "value_only"},
		{"USD 10 [1].", "grounded", "found"},
	} {
		r := Check(tc.answer, citations)
		if r.Status != tc.status || len(r.Claims) == 0 || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Status != tc.first {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
	}
}

func TestLeadingDecimalAmountsKeepCurrency(t *testing.T) {
	for _, tc := range []struct{ answer, source, status string }{
		{"€.50", "$0.50", "value_only"}, {"-€.50", "EUR -0.5", "found"}, {".50 EUR", "€0.5", "found"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "amount" || r.Claims[0].Figures[0].Status != tc.status {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
	}
}

func TestLowercaseProseAfterNumberIsNotCurrency(t *testing.T) {
	r := Check("40 all told", []Citation{{Index: 1, Text: "40"}})
	if r.Status != "grounded" || len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "number" {
		t.Fatalf("%+v", r)
	}
}

func TestLowercaseProseBeforeNumberIsNotCurrency(t *testing.T) {
	r := Check("all 20 invoices", []Citation{{Index: 1, Text: "20 invoices"}})
	if r.Status != "grounded" {
		t.Fatalf("%+v", r)
	}
	if len(r.Claims[0].Figures) != 1 || r.Claims[0].Figures[0].Kind != "number" {
		t.Fatalf("%+v", r)
	}
}

func TestAdjacentAmountsRetainEachCurrency(t *testing.T) {
	r := Check("$20 $30", []Citation{{Index: 1, Text: "20 and $30"}})
	if r.Status != "ungrounded" || len(r.Claims[0].Figures) != 2 || r.Claims[0].Figures[0].Status != "value_only" {
		t.Fatalf("%+v", r)
	}
}

func TestSourceDateContainsItsWrittenYear(t *testing.T) {
	r := Check("2026", []Citation{{Index: 1, Text: "March 15, 2026"}})
	if r.Status != "grounded" {
		t.Fatalf("%+v", r)
	}
}

func TestDateComponentsExcludeHiddenLinkDestinations(t *testing.T) {
	for _, tc := range []struct{ answer, status string }{{"500", "ungrounded"}, {"2026", "grounded"}, {"15", "grounded"}} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: "[March](x/500) 15, 2026"}})
		if r.Status != tc.status {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
	}
}

func TestDateComponentsUseProjectedText(t *testing.T) {
	for _, tc := range []struct{ answer, status string }{{"2026", "grounded"}, {"20", "ungrounded"}, {"26", "ungrounded"}} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: "20**26**-03-15"}})
		if r.Status != tc.status {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
	}
}

func TestNormalizedFiguresUseDisplayedMarkdownReferences(t *testing.T) {
	for _, tc := range []struct{ answer, source, figure string }{
		{"&#50;026-03-15 [1]", "March 15, 2026", "&#50;026-03-15"},
		{"&#36;20 [1]", "USD 20", "&#36;20"},
		{"2026", "&#50;026-03-15", "2026"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
		f := r.Claims[0].Figures[0]
		if f.Text != tc.figure || string([]rune(tc.answer)[f.Start:f.End]) != tc.figure {
			t.Fatalf("%q: %+v", tc.answer, f)
		}
	}
}

func TestNormalizedFiguresRespectUnicodeWordBoundaries(t *testing.T) {
	for _, tc := range []struct{ answer, source string }{
		{"2026-03-15", "2026-03-15\u0301"}, {"€20", "€20²"}, {"€20", "\u0301€20"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if r.Status == "grounded" {
			t.Fatalf("%q / %q: %+v", tc.answer, tc.source, r)
		}
	}
}

func TestNormalizedFiguresRemainWholeAcrossLinks(t *testing.T) {
	for _, tc := range []struct{ answer, source, figure string }{
		{"$[20](url) [1]", "USD 20", "$[20"},
		{"USD 20 [1]", "$[20](url)", "USD 20"},
		{"2026-[03](url)-15 [1]", "March 15, 2026", "2026-[03](url)-15"},
		{"2026 [1]", "20[26](url)-03-15", "2026"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
		f := r.Claims[0].Figures[0]
		if f.Text != tc.figure || f.Text != string([]rune(tc.answer)[f.Start:f.End]) {
			t.Fatalf("%q: %+v", tc.answer, f)
		}
	}
}

func TestPlainNumbersMatchZeroPaddedSourceDateComponents(t *testing.T) {
	for _, answer := range []string{"3", "5"} {
		r := Check(answer, []Citation{{Index: 1, Text: "2026-03-05"}})
		if r.Status != "grounded" {
			t.Fatalf("%q: %+v", answer, r)
		}
	}
}

func TestNormalizedFiguresRespectLiteralEscapePolicy(t *testing.T) {
	for _, tc := range []struct{ answer, source, status string }{
		{"`$1\\.5` [1]", "$1.5", "ungrounded"},
		{"$1.5 [1]", "`$1\\.5`", "ungrounded"},
		{`\$1\.5 [1]`, "$1.5", "grounded"},
		{"`2026\\-03-15` [1]", "2026-03-15", "ungrounded"},
		{`2026\-03-15 [1]`, "2026-03-15", "grounded"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if r.Status != tc.status {
			t.Fatalf("%q / %q: %+v", tc.answer, tc.source, r)
		}
	}
}

func TestNormalizedFiguresRemainWholeAcrossInlineCode(t *testing.T) {
	for _, tc := range []struct{ answer, source, figure string }{
		{"$`20` [1]", "USD 20", "$`20"},
		{"USD 20 [1]", "$`20`", "USD 20"},
		{"2026-`03`-15 [1]", "March 15, 2026", "2026-`03`-15"},
		{"2026 [1]", "20`26`-03-15", "2026"},
	} {
		r := Check(tc.answer, []Citation{{Index: 1, Text: tc.source}})
		if r.Status != "grounded" || len(r.Claims) != 1 || len(r.Claims[0].Figures) != 1 {
			t.Fatalf("%q: %+v", tc.answer, r)
		}
		f := r.Claims[0].Figures[0]
		if f.Text != tc.figure || f.Text != string([]rune(tc.answer)[f.Start:f.End]) {
			t.Fatalf("%q: %+v", tc.answer, f)
		}
	}
}
