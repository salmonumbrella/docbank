// Package conflicts extracts comparable figures from passage text.
package conflicts

import (
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Period describes inclusive date bounds stated by a clause. Empty bounds are unknown.
type Period struct{ From, To string }

// Figure describes a figure and its clause. All offsets are rune offsets into the input.
// Value and EndValue are exact normalized decimals, dates, or identifier strings;
// EndValue is set only for ranges. Date precision is "day" or "month".
type Figure struct {
	Kind, Unit, Value, EndValue, Qualifier, Precision, Text string
	Start, End                                              int
	Clause                                                  string
	ClauseStart, ClauseEnd                                  int
	Partial                                                 bool
	Period                                                  Period
	// Modifier preserves the adjective in the bounded count grammar.
	Modifier string
	// acceptedDates are byte spans in the normalized clause. They retain the
	// extraction decision for dates omitted from the public figure list.
	acceptedDates []dateSpan
}

type dateSpan struct{ start, end int }

type clause struct {
	start, end int
	partial    bool
}
type candidate struct {
	start, end int
	f          Figure
	priority   int
}

var (
	// Longer number words precede their prefixes (fourteen before four).
	number                 = numberPattern()
	numRange               = `(?:between\s+` + number + `\s+and\s+` + number + `|` + number + `(?:(?:\s*(?:-|–)\s*|\s+to\s+)` + number + `)?)`
	currencyCodes          = `(?:USD|EUR|GBP|JPY|CNY|RUB|CAD|AUD|CHF)`
	moneyRE                = regexp.MustCompile(`(?i)(?:[$€£¥]|\b` + currencyCodes + `\s*)\s*(` + numRange + `)|\b(` + numRange + `)\s*(dollars?|euros?|pounds?|yuan|yen|roubles?|rubles?|` + currencyCodes + `)\b|\b(` + numRange + `)\s*([$€£¥])`)
	currencyCodeRE         = regexp.MustCompile(`(?i)\b` + currencyCodes + `\b`)
	currencyCodePrefixRE   = regexp.MustCompile(`(?i)\b` + currencyCodes + `\s*$`)
	currencyCodeSuffixRE   = regexp.MustCompile(`(?i)^\s*` + currencyCodes + `\b`)
	currencySymbolPrefixRE = regexp.MustCompile(`[$€£¥]\s*$`)
	currencySymbolSuffixRE = regexp.MustCompile(`^\s*[$€£¥]`)
	unitRE                 = regexp.MustCompile(`(?i)\b(` + numRange + `)\s*(percentage\s+points?|percent|per\s+cent|%|business\s+days?|working\s+days?|calendar\s+days?|minutes?|mins?|hours?|hrs?|days?|weeks?|months?|years?)\b|\b(` + numRange + `)\s*(%)`)
	dateRE                 = regexp.MustCompile(`(?i)\b(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{1,2}/[0-9]{1,2}/[0-9]{4}|[0-9]{1,2}\s+(?:january|february|march|april|may|june|july|august|september|october|november|december)\s+[0-9]{4}|(?:january|february|march|april|may|june|july|august|september|october|november|december)\s+(?:[0-9]{1,2},?\s+)?[0-9]{4})\b`)
	identifierRE           = regexp.MustCompile(`(?i)\b(account|policy|reference|invoice|order|contract|case|ticket|serial|registration|member|customer|iban|tax\s+id|vat)(?:\s+(?:number|no\.?|id))?\s*(?::|\bis\b)\s*([a-z0-9*][a-z0-9*-]{3,})`)
	identifierExplicitRE   = regexp.MustCompile(`(?i)\b(?:number|no\.?|id)\s*(?::|\bis\b)\s*$`)
	identifierMaskRE       = regexp.MustCompile(`(?i)x{2,}`)
	identifierSuffixMaskRE = regexp.MustCompile(`(?i)^[\s,:(\["'‘’“”]+ending\s+in\b`)
	// Only listed nouns and modifiers participate: arbitrary prose is not parsed as a count.
	countRE               = regexp.MustCompile(`(?i)\b(` + numRange + `)\s+(?:(free|paid|additional|extra)\s+)?(relocations?|devices?|items?|users?|accounts?|payments?|calls?|visits?|tickets?|orders?|requests?|documents?|connections?)\b`)
	negationRE            = regexp.MustCompile(`(?i)(?:\b(?:not|cannot|never|neither|nor|no|without)\b|n['’‘ʼ]t\b|\bno\s+longer\b)`)
	approximation         = `(?:about|approximately|around|roughly|nearly|almost|circa|estimated)`
	approximationPrefixRE = regexp.MustCompile(`(?i)(?:\b` + approximation + `|~)(?:\s+(?:exactly|a\s+total\s+of))?[\s(),:"'‘’“”]*$`)
	approximationSuffixRE = regexp.MustCompile(`(?i)^[\s,()\]"'‘’“”»]*(?:` + approximation + `\b|~)\s*\)*`)
	fractionWords         = `(?:half|quarter|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth)`
	fractionSuffixRE      = regexp.MustCompile(`(?i)^(?:a\s+)?` + fractionWords + `\b`)
	fractionPrefixRE      = regexp.MustCompile(`(?i)\b(?:(?:a\s+)?` + fractionWords + `s?|[0-9]+\s*/\s*[0-9]+)\s+of\s*$`)
	exampleRE             = regexp.MustCompile(`(?i)(?:\bfor\s+example\b|\be\.g\.|\bsuch\s+as\b|\bfor\s+instance\b)`)
	wordRE                = regexp.MustCompile(`(?i)\b` + number + `\b`)
)

// MaxPassageBytes bounds extraction work. Longer passages are skipped whole.
const MaxPassageBytes = 4096

const dateKind = "date"

// Extract returns figures ordered by their exact spans. Dates and identifiers take
// precedence over other recognizers; ambiguous overlapping matches are omitted.
func Extract(text string) []Figure {
	if len(text) > MaxPassageBytes {
		return nil
	}
	var result []Figure
	for _, cl := range splitClauses(text) {
		original := text[cl.start:cl.end]
		s, offsets := normalizeClause(original)
		if strings.Contains(s, "|") || hasNegation(s) || exampleRE.MatchString(s) || nonLatinOnly(s) {
			continue
		}
		var cs, checks []candidate
		identifierSpans := identifierRE.FindAllStringIndex(s, -1)
		identifierSpans = slices.DeleteFunc(identifierSpans, func(span []int) bool {
			match := identifierRE.FindStringSubmatchIndex(s[span[0]:span[1]])
			token := span[0] + match[4]
			label := s[span[0]:token]
			if identifierExplicitRE.MatchString(label) {
				return false
			}
			for _, re := range []*regexp.Regexp{moneyRE, unitRE, countRE} {
				if loc := re.FindStringIndex(s[token:]); loc != nil && loc[0] == 0 && loc[1] > span[1]-token {
					return true
				}
			}
			return false
		})
		unsupportedNumeric := false
		add := func(re *regexp.Regexp, priority int, parse func(string, []string) (Figure, bool)) {
			search := s
			if re == moneyRE {
				search = maskDateShapes(s)
			}
			for _, loc := range re.FindAllStringSubmatchIndex(search, -1) {
				start, end := loc[0], loc[1]
				if re == identifierRE && !slices.ContainsFunc(identifierSpans, func(span []int) bool { return span[0] == start && span[1] == end }) {
					continue
				}
				// Resolve contained lower-priority fragments before they can
				// reject the clause as an unsupported numeric expression.
				if (priority > 0 || re == dateRE) && slices.ContainsFunc(identifierSpans, func(span []int) bool {
					if span[0] > start || end > identifierTokenEnd(s, span[1]) {
						return false
					}
					_, valid := parseIdentifier("", identifierRE.FindStringSubmatch(s[span[0]:span[1]]))
					return priority > 0 || !valid || !identifierEndsAt(s[span[1]:])
				}) || slices.ContainsFunc(cs, func(c candidate) bool {
					return c.priority < priority && c.start <= start && end <= c.end
				}) {
					continue
				}
				if _, ok := offsets[start]; !ok {
					continue
				}
				if _, ok := offsets[end]; !ok {
					continue
				}
				if start > 0 {
					r, _ := utf8.DecodeLastRuneInString(s[:start])
					if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ',' || r == '.' {
						unsupportedNumeric = unsupportedNumeric || priority > 0 && (r == ',' || r == '.')
						continue
					}
				}
				if end+1 < len(s) && (s[end] == ',' || s[end] == '.') && s[end+1] >= '0' && s[end+1] <= '9' {
					unsupportedNumeric = unsupportedNumeric || priority > 0
					continue
				}
				parts := make([]string, len(loc)/2)
				for i := range parts {
					if loc[2*i] >= 0 {
						parts[i] = s[loc[2*i]:loc[2*i+1]]
					}
				}
				f, ok := parse(s[start:end], parts)
				if !ok {
					unsupportedNumeric = unsupportedNumeric || priority > 0
					continue
				}
				if re == moneyRE && (strings.ContainsAny(f.Unit, "$€£¥") && (currencyCodePrefixRE.MatchString(s[:start]) || currencyCodeSuffixRE.MatchString(s[end:])) ||
					currencyCodeRE.MatchString(f.Unit) && (currencySymbolPrefixRE.MatchString(s[:start]) || currencySymbolSuffixRE.MatchString(s[end:]))) {
					unsupportedNumeric = true
					continue
				}
				if (f.Kind == "identifier" && !identifierEndsAt(s[end:])) || (f.Kind == dateKind && (!dateEndsAt(s[end:]) || dateContinuedPrefix(s[:start]))) {
					continue
				}
				checks = append(checks, candidate{start, end, f, priority})
				f.Qualifier, ok = qualifier(s[:start])
				if !ok || approximationSuffixRE.MatchString(s[end:]) || priority > 0 && (postfixBound(s[end:]) || punctuatedBound(s[:start]) || parenthesizedMoney(f, s[:start], s[end:]) || uncertaintyAdjacent(s[:start], s[end:])) {
					prefix := approximationPrefixRE.ReplaceAllString(strings.TrimSpace(s[:start]), "")
					unsupportedNumeric = unsupportedNumeric || !ok && compoundNumberPrefix(prefix)
					continue
				}
				cs = append(cs, candidate{start, end, f, priority})
			}
		}
		add(dateRE, 0, parseDate)
		add(identifierRE, 0, parseIdentifier)
		add(moneyRE, 1, parseMoney)
		add(unitRE, 1, parseUnit)
		add(countRE, 2, parseCount)
		cs = omitUncertainGroups(s, checks, cs)
		// A partial numeric expression makes the whole clause unsafe, including
		// later figures that would otherwise survive dropping its first part.
		if unsupportedNumeric || unsafeCandidates(s, checks, cs) {
			continue
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].start < cs[j].start })
		rejected := make([]bool, len(cs))
		for i, c := range cs {
			for j := i + 1; j < len(cs) && cs[j].start < c.end; j++ {
				other := cs[j]
				if c.priority >= other.priority {
					rejected[i] = true
				}
				if other.priority >= c.priority {
					rejected[j] = true
				}
			}
		}
		var selected []candidate
		for i, c := range cs {
			if !rejected[i] {
				selected = append(selected, c)
			}
		}
		period := Period{}
		var acceptedDates []dateSpan
		nonDate := false
		for _, c := range selected {
			if c.f.Kind != dateKind {
				nonDate = true
			} else {
				acceptedDates = append(acceptedDates, dateSpan{c.start, c.end})
			}
		}
		periodOK := true
		periodUncertain := false
		if nonDate {
			for _, c := range selected {
				if c.f.Kind != dateKind {
					continue
				}
				prefix := strings.ToLower(strings.Join(strings.Fields(s[:c.start]), " "))
				var bound *string
				switch {
				case endsWord(prefix, "from", "since", "effective", "as of", "starting", "beginning"):
					bound = &period.From
				case endsWord(prefix, "until", "till", "to", "through", "ending", "before"):
					bound = &period.To
				}
				if bound != nil {
					value := c.f.Value
					periodUncertain = periodUncertain || slices.ContainsFunc(checks, func(prev candidate) bool {
						return prev.f.Kind == "duration" && prev.end <= c.start && dateBeforeConnectorRE.MatchString(s[prev.end:c.start])
					})
					if endsWord(prefix, "before", "prior to") {
						var ok bool
						value, ok = inclusiveBefore(c.f)
						periodUncertain = periodUncertain || !ok
					}
					if *bound != "" && *bound != value {
						periodOK = false
						break
					}
					*bound = value
				}
			}
		}
		if !periodOK {
			continue
		}
		if periodUncertain || invertedPeriod(period) || independentPeriodScopes(s, periodScopeCandidates(selected, checks)) {
			period = Period{}
		}
		for _, c := range selected {
			if nonDate && c.f.Kind == dateKind {
				continue
			}
			f := c.f
			start, end := offsets[c.start], offsets[c.end]
			f.Text = original[start:end]
			f.Start = utf8.RuneCountInString(text[:cl.start+start])
			f.End = f.Start + utf8.RuneCountInString(f.Text)
			f.Clause = original
			f.ClauseStart = utf8.RuneCountInString(text[:cl.start])
			f.ClauseEnd = f.ClauseStart + utf8.RuneCountInString(original)
			f.Partial = cl.partial
			f.Period = period
			f.acceptedDates = acceptedDates
			result = append(result, f)
		}
	}
	return result
}

const calendarExclusion = `(?:excluding|except(?:\s+for)?)\b`

var calendarExclusionRE = regexp.MustCompile(`(?i)^[\s,()\]"'‘’“”»]+` + calendarExclusion)
var numericExclusionRE = regexp.MustCompile(`(?i)\b` + calendarExclusion + `\s+(?:the\s+)?[\s(]*$`)
var exclusionJoinRE = regexp.MustCompile(`(?i)^[\s,()\]"'‘’“”»]+` + calendarExclusion + `.*?(\s+and\s+|,\s*)$`)
var dateBeforeConnectorRE = regexp.MustCompile(`(?i)^[\s,()\]"'”’»]+(?:` + calendarExclusion + `.*?[\s,()\]"'”’»]+)?(?:before|prior\s+to)\s+$`)

func invertedPeriod(p Period) bool {
	if p.From == "" || p.To == "" {
		return false
	}
	from, to := p.From, p.To
	if len(from) == len("2006-01") {
		from += "-01"
	}
	if len(to) == len("2006-01") {
		t, err := time.Parse("2006-01", to)
		if err != nil {
			return true
		}
		to = t.AddDate(0, 1, -1).Format("2006-01-02")
	}
	return from > to
}

// Scope sees accepted dates and recognized assertions, even those omitted as uncertain.
func periodScopeCandidates(selected, checks []candidate) []candidate {
	var scope []candidate
	for _, c := range selected {
		if c.f.Kind == dateKind {
			scope = append(scope, c)
		}
	}
	for _, c := range checks {
		if c.f.Kind == dateKind || slices.ContainsFunc(selected, func(accepted candidate) bool {
			return accepted.priority < c.priority && accepted.start < c.end && c.start < accepted.end
		}) {
			continue
		}
		scope = append(scope, c)
	}
	sort.SliceStable(scope, func(i, j int) bool { return scope[i].start < scope[j].start })
	return scope
}

// A date between assertions cannot safely scope figures on both sides.
func independentPeriodScopes(s string, selected []candidate) bool {
	firstDate, firstConnector := "", ""
	lastDateEnd := -1
	sameBounds := true
	dateCount := 0
	for _, c := range selected {
		if c.f.Kind != dateKind {
			continue
		}
		if lastDateEnd >= 0 && strings.EqualFold(strings.TrimSpace(s[lastDateEnd:c.start]), "or") {
			return true
		}
		lastDateEnd = c.end
		dateCount++
		prefix := strings.ToLower(strings.Join(strings.Fields(s[:c.start]), " "))
		connector := ""
		for _, word := range []string{"from", "since", "effective", "as of", "starting", "beginning", "until", "till", "to", "through", "ending", "before"} {
			if endsWord(prefix, word) {
				connector = word
				break
			}
		}
		if firstDate == "" {
			firstDate, firstConnector = c.f.Value, connector
		} else if firstDate != c.f.Value || firstConnector != connector {
			sameBounds = false
		}
	}
	if sameBounds && dateCount > 1 {
		return false
	}
	firstFigureEnd := -1
	dateAfterFigure := false
	for _, c := range selected {
		if c.f.Kind == dateKind {
			dateAfterFigure = dateAfterFigure || firstFigureEnd >= 0
		} else if firstFigureEnd < 0 {
			firstFigureEnd = c.end
		}
	}
	if dateAfterFigure {
		masked := maskAcceptedDates(s, selected)
		for _, c := range selected {
			if c.f.Kind == dateKind || c.start < firstFigureEnd {
				continue
			}
			for _, word := range strings.FieldsFunc(strings.ToLower(masked[firstFigureEnd:c.start]), func(r rune) bool { return !unicode.IsLetter(r) }) {
				if word != "and" && word != "per" && word != "each" && word != "a" {
					return true
				}
			}
			firstFigureEnd = c.end
		}
	}
	seenFigure := false
	lastDateEnd = -1
	for _, c := range selected {
		if c.f.Kind == dateKind {
			if seenFigure {
				lastDateEnd = c.end
			}
			continue
		}
		if lastDateEnd >= 0 {
			tail := strings.TrimSpace(strings.ToLower(s[lastDateEnd:c.start]))
			tail = strings.TrimSpace(strings.TrimPrefix(tail, "and "))
			if strings.IndexFunc(tail, unicode.IsLetter) >= 0 {
				return true
			}
			lastDateEnd = -1
		}
		seenFigure = true
	}
	return false
}

func inclusiveBefore(f Figure) (string, bool) {
	layout := "2006-01-02"
	if f.Precision == "month" {
		layout = "2006-01"
	}
	t, err := time.Parse(layout, f.Value)
	if err != nil {
		return "", false
	}
	t = t.AddDate(0, 0, -1)
	if t.Year() < 1 {
		return "", false
	}
	return t.Format("2006-01-02"), true
}

// Protect lower-priority matches through the rest of a continued identifier token.
func identifierTokenEnd(s string, end int) int {
	if tokenEndsAt(s[end:]) {
		return end
	}
	for end < len(s) {
		field := strings.TrimLeftFunc(s[end:], unicode.IsSpace)
		start := len(s) - len(field)
		n := strings.IndexFunc(field, unicode.IsSpace)
		if n < 0 {
			n = len(field)
		}
		if start > end && !strings.ContainsAny(field[:n], "0123456789") {
			break
		}
		end = start + n
	}
	return end
}

// Mask accepted dates when analyzing period relationships.
func maskAcceptedDates(s string, cs []candidate) string {
	b := []byte(s)
	for _, c := range cs {
		if c.f.Kind == dateKind {
			for i := c.start; i < c.end; i++ {
				b[i] = 0
			}
		}
	}
	return string(b)
}

// Keep date-shaped bytes from being consumed as a numeric range endpoint.
func maskDateShapes(s string) string {
	b := []byte(s)
	for _, span := range dateRE.FindAllStringIndex(s, -1) {
		for i := span[0]; i < span[1]; i++ {
			b[i] = 0
		}
	}
	return string(b)
}

// normalizeClause maps NFKC segment boundaries back to original byte offsets.
// Matches that cut through a normalization segment are discarded.
func normalizeClause(s string) (string, map[int]int) {
	var it norm.Iter
	it.InitString(norm.NFKC, s)
	var b strings.Builder
	offsets := map[int]int{0: 0}
	for !it.Done() {
		segment := it.Next()
		b.Write(segment)
		offsets[b.Len()] = it.Pos()
	}
	return b.String(), offsets
}

// Bound phrases and the identifier abbreviation are not negative claims.
func hasNegation(s string) bool {
	for _, loc := range negationRE.FindAllStringIndex(s, -1) {
		word := strings.ToLower(s[loc[0]:loc[1]])
		tail := s[loc[1]:]
		if word == "no" && (strings.HasPrefix(tail, ".") || noBoundRE.MatchString(tail)) {
			continue
		}
		return true
	}
	return false
}

var noBoundRE = regexp.MustCompile(`(?i)^\s+(?:more|less)\s+than\b`)

func nonLatinOnly(s string) bool {
	hasLetter := false
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			return false
		}
		hasLetter = hasLetter || unicode.IsLetter(r)
	}
	return hasLetter
}
func endsWord(s string, words ...string) bool {
	for _, w := range words {
		if s == w || strings.HasSuffix(s, " "+w) {
			return true
		}
	}
	return false
}

var boundWords = []struct {
	words []string
	value string
}{{[]string{"less than or equal to", "fewer than or equal to", "up to", "at most", "no more than", "maximum of", "maximum", "max"}, "≤"}, {[]string{"greater than or equal to", "more than or equal to", "at least", "minimum of", "minimum", "no less than"}, "≥"}, {[]string{"more than", "greater than", "over", "exceeding"}, ">"}, {[]string{"less than", "under", "fewer than"}, "<"}, {[]string{"within"}, "within"}}

func postfixBound(s string) bool {
	return postfixBoundEnd(s) > 0
}

var postfixBoundLeadRE = regexp.MustCompile(`(?i)^[\s,()\]"'‘’“”»]*(?:(?:at\s+)?(?:a|the)\s+)?`)

// Compile the shared bound vocabulary once; match indices remain byte offsets.
var postfixBounds = func() []*regexp.Regexp {
	result := []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:and\s+(?:up|above|over)|at\s+(?:minimum|maximum))`)}
	for _, q := range boundWords {
		for _, word := range q.words {
			if word == "over" || word == "under" || word == "within" || word == "exceeding" {
				continue
			}
			result = append(result, regexp.MustCompile(`(?i)^`+strings.Join(strings.Fields(word), `\s+`)))
		}
	}
	return result
}()

func postfixBoundEnd(s string) int {
	lead := postfixBoundLeadRE.FindStringIndex(s)[1]
	tail := s[lead:]
	for _, re := range postfixBounds {
		if loc := re.FindStringIndex(tail); loc != nil {
			rest := tail[loc[1]:]
			r, _ := utf8.DecodeRuneInString(rest)
			if rest == "" || !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				return lead + loc[1]
			}
		}
	}
	return 0
}

func punctuatedBound(prefix string) bool {
	trimmed := strings.TrimRightFunc(prefix, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
	if trimmed == strings.TrimSpace(prefix) {
		return false
	}
	_, q := splitQualifier(strings.ToLower(strings.Join(strings.Fields(trimmed), " ")))
	return q != ""
}

func parenthesizedMoney(f Figure, prefix, suffix string) bool {
	return f.Kind == "money" && strings.HasSuffix(strings.TrimSpace(prefix), "(") && strings.HasPrefix(strings.TrimSpace(suffix), ")")
}

var uncertaintyPrefixRE = regexp.MustCompile(`(?i)\bgive\s+or\s+take\s*$`)
var uncertaintySuffixRE = regexp.MustCompile(`(?i)^\s*,?\s*give\s+or\s+take\b`)

func uncertaintyAdjacent(prefix, suffix string) bool {
	return uncertaintyPrefixRE.MatchString(prefix) || uncertaintySuffixRE.MatchString(suffix)
}

func qualifier(prefix string) (string, bool) {
	p, q := splitQualifier(strings.ToLower(strings.Join(strings.Fields(prefix), " ")))
	return q, !approximationPrefixRE.MatchString(p)
}

// splitQualifier keeps range context visible before a bound such as "at most".
func splitQualifier(p string) (string, string) {
	p = strings.TrimRightFunc(p, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`:"'‘’“”`, r) })
	for _, q := range []struct{ ascii, value string }{{"<=", "≤"}, {">=", "≥"}} {
		if trimmed, ok := strings.CutSuffix(p, q.ascii); ok {
			return strings.TrimSpace(trimmed), q.value
		}
	}
	for _, symbol := range []string{"≤", "≥", ">", "<"} {
		if trimmed, ok := strings.CutSuffix(p, symbol); ok {
			return strings.TrimSpace(trimmed), symbol
		}
	}
	for _, q := range boundWords {
		for _, word := range q.words {
			if endsWord(strings.ReplaceAll(p, "(", " "), word) {
				return strings.TrimSpace(strings.TrimSuffix(p, word)), q.value
			}
		}
	}
	return p, ""
}
func splitClauses(s string) []clause {
	var out []clause
	start := 0
	partial := false
	emit := func(end int, nextPartial bool) {
		a, b := start, end
		for a < b {
			r, n := utf8.DecodeRuneInString(s[a:b])
			if !unicode.IsSpace(r) {
				break
			}
			a += n
		}
		for b > a {
			r, n := utf8.DecodeLastRuneInString(s[a:b])
			if !unicode.IsSpace(r) {
				break
			}
			b -= n
		}
		if a < b {
			out = append(out, clause{a, b, partial || nextPartial})
		}
		if a < b {
			partial = nextPartial
		} else {
			partial = partial || nextPartial
		}
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if strings.HasPrefix(s[i:], "...") {
			emit(i, true)
			for i < len(s) && s[i] == '.' {
				i++
			}
			start = i
			continue
		}
		if r == '…' {
			emit(i, true)
			i += n
			start = i
			continue
		}
		if r == ';' || r == '\n' && hardLineBreak(s, i) {
			emit(i, false)
			i += n
			start = i
			continue
		}
		if r == '.' || r == '!' || r == '?' {
			next := i + n
			for next < len(s) {
				closer, size := utf8.DecodeRuneInString(s[next:])
				if !strings.ContainsRune(closingPunctuation, closer) {
					break
				}
				next += size
			}
			nextRune, _ := utf8.DecodeRuneInString(s[next:])
			if next == len(s) || unicode.IsSpace(nextRune) {
				prefix := strings.ToLower(s[start : i+n])
				abbrev := false
				for _, a := range []string{"e.g.", "i.e.", "no.", "mr.", "mrs.", "dr."} {
					r, _ := utf8.DecodeLastRuneInString(strings.TrimSuffix(prefix, a))
					if strings.HasSuffix(prefix, a) && !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && (a != "no." || identifierAbbreviation(s[start:], i-start)) {
						abbrev = true
						break
					}
				}
				if !abbrev {
					emit(next, false)
					start = next
				}
			}
		}
		i += n
	}
	emit(len(s), false)
	return out
}

// Blank lines and list or table rows separate independent statements.
var listRowRE = regexp.MustCompile(`^\s*(?:[-*+]\s+|[0-9]+[.)]\s+)`)

func hardLineBreak(s string, i int) bool {
	start := strings.LastIndexByte(s[:i], '\n') + 1
	end := strings.IndexByte(s[i+1:], '\n')
	if end < 0 {
		end = len(s)
	} else {
		end += i + 1
	}
	before, after := s[start:i], s[i+1:end]
	return strings.TrimSpace(before) == "" || strings.TrimSpace(after) == "" ||
		strings.Contains(before, "|") || strings.Contains(after, "|") ||
		listRowRE.MatchString(before) || listRowRE.MatchString(after)
}

// Identifier labels own No.; a standalone answer ends its sentence.
func identifierAbbreviation(s string, dot int) bool {
	return slices.ContainsFunc(identifierRE.FindAllStringIndex(s, -1), func(loc []int) bool {
		return loc[0] <= dot && dot < loc[1]
	})
}

var numberWords = map[string]string{"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18", "nineteen": "19", "twenty": "20"}

func numberPattern() string {
	words := make([]string, 0, len(numberWords))
	for word := range numberWords {
		words = append(words, word)
	}
	sort.Slice(words, func(i, j int) bool {
		if len(words[i]) == len(words[j]) {
			return words[i] < words[j]
		}
		return len(words[i]) > len(words[j])
	})
	return `(?:[0-9]+(?:,[0-9]{3})*(?:\.[0-9]+)?|` + strings.Join(words, "|") + `)`
}

const numericOperators = "−+*/⁄×÷±∓"

func numericOperator(r rune) bool {
	return unicode.Is(unicode.Dash, r) || strings.ContainsRune(numericOperators, r)
}

// Do not extract a supported suffix from an unsupported compound numeral.
func compoundNumberPrefix(prefix string) bool {
	if groupedNumberPrefix(strings.TrimRightFunc(prefix, unicode.IsSpace)) {
		return true
	}
	prefix = strings.TrimRightFunc(prefix, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`("'‘’“”`, r) })
	prefix, _ = splitQualifier(strings.ToLower(strings.Join(strings.Fields(prefix), " ")))
	if endsWord(prefix, "out of") {
		prefix = strings.TrimSpace(strings.TrimSuffix(prefix, "out of"))
	}
	r, _ := utf8.DecodeLastRuneInString(prefix)
	if unicode.IsDigit(r) || numericOperator(r) || groupedNumberPrefix(prefix) || endsWord(prefix, "between", "minus", "negative", "plus", "less") || fractionPrefixRE.MatchString(prefix) {
		return true
	}
	i := strings.LastIndexFunc(prefix, func(r rune) bool { return !unicode.IsLetter(r) })
	connector := strings.ToLower(prefix[i+1:])
	if connector == "and" || connector == "or" || connector == "point" || connector == "to" || connector == "through" || connector == "until" || connector == "till" {
		prefix = strings.TrimRightFunc(prefix[:i+1], unicode.IsSpace)
		if connector == "and" && endsTypedFigure(prefix) {
			return false
		}
		r, _ = utf8.DecodeLastRuneInString(prefix)
		if unicode.IsDigit(r) {
			return true
		}
		i = strings.LastIndexFunc(prefix, func(r rune) bool { return !unicode.IsLetter(r) })
	}
	return numeralWord(prefix[i+1:])
}

// Approximation scopes coordinated figures; bounds and exclusions scope duration groups.
func omitUncertainGroups(s string, checks, cs []candidate) []candidate {
	var figures []candidate
	for _, c := range checks {
		if c.f.Kind == "duration" || c.f.Kind == "money" || c.f.Kind == "percent" || c.f.Kind == "count" {
			figures = append(figures, c)
		}
	}
	sort.SliceStable(figures, func(i, j int) bool { return figures[i].start < figures[j].start })
	for i := 0; i < len(figures); {
		j := i + 1
		for j < len(figures) && figures[j].f.Kind == figures[i].f.Kind && figures[j-1].end <= figures[j].start && durationJoined(s[figures[j-1].end:figures[j].start]) {
			j++
		}
		uncertain, excluded := false, false
		if numericExclusionRE.MatchString(s[:figures[i].start]) {
			excluded = true
		}
		for _, c := range figures[i:j] {
			q, exact := qualifier(s[:c.start])
			uncertain = uncertain || !exact || approximationSuffixRE.MatchString(s[c.end:]) || uncertaintyAdjacent(s[:c.start], s[c.end:])
			if c.f.Kind == "duration" {
				uncertain = uncertain || q != "" || postfixBound(s[c.end:])
				excluded = excluded || calendarExclusionRE.MatchString(s[c.end:])
			}
		}
		if excluded || uncertain && j-i > 1 {
			cs = slices.DeleteFunc(cs, func(c candidate) bool {
				return c.f.Kind == figures[i].f.Kind && c.start >= figures[i].start && c.end <= figures[j-1].end
			})
		}
		i = j
	}
	return cs
}

var durationJoinRE = regexp.MustCompile(`(?i)^[\s)"'‘’“”]*(?:,\s*(?:and\s+)?|and\s+)[\s("'‘’“”]*$`)

func durationJoined(s string) bool {
	return durationJoinRE.MatchString(durationJoinTail(s))
}

func durationJoinTail(s string) string {
	if loc := exclusionJoinRE.FindStringSubmatchIndex(s); loc != nil {
		s = s[loc[2]:]
	} else if loc := approximationSuffixRE.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	} else if end := postfixBoundEnd(s); end > 0 {
		s = s[end:]
	}
	return s
}

// A grouping separator between digits is not a scalar boundary.
func groupedNumberPrefix(prefix string) bool {
	r, size := utf8.DecodeLastRuneInString(prefix)
	if !strings.ContainsRune("_'’‘ʼ", r) {
		return false
	}
	r, _ = utf8.DecodeLastRuneInString(prefix[:len(prefix)-size])
	return unicode.IsDigit(r)
}

func groupedNumberSuffix(suffix string) bool {
	r, size := utf8.DecodeRuneInString(suffix)
	if !strings.ContainsRune("_'’‘ʼ", r) {
		return false
	}
	r, _ = utf8.DecodeRuneInString(suffix[size:])
	return unicode.IsDigit(r)
}

// An accepted date immediately before a figure is not a compound numeral.
func compoundPrefixAfterDates(prefix string, cs []candidate) bool {
	trimmed := strings.TrimRightFunc(prefix, unicode.IsSpace)
	if rest, ok := strings.CutSuffix(trimmed, ","); ok {
		before := strings.TrimRightFunc(rest, unicode.IsSpace)
		r, _ := utf8.DecodeLastRuneInString(before)
		if unicode.IsDigit(r) {
			return !slices.ContainsFunc(cs, func(c candidate) bool { return c.end == len(before) })
		}
	}
	if len(trimmed) < len(prefix) && slices.ContainsFunc(cs, func(c candidate) bool {
		return c.f.Kind == dateKind && c.end == len(trimmed)
	}) {
		return false
	}
	return compoundNumberPrefix(prefix)
}

// A numeric denominator remains a figure for later rate classification.
func compoundPrefixWithRates(s string, start int, cs []candidate) bool {
	for _, c := range cs {
		if c.priority > 0 && c.end <= start {
			if n := slashRateEnd(s[c.end:]); n > 0 && start < c.end+n {
				return false
			}
		}
	}
	return compoundPrefixAfterDates(s[:start], cs)
}

// Validate relationships only after all recognizers have collected candidates.
func unsafeCandidates(s string, checks, cs []candidate) bool {
	for _, left := range checks {
		for _, right := range checks {
			if left.priority > 0 && right.priority > 0 && left.end < right.start && comparativeRE.MatchString(s[left.end:right.start]) {
				return true
			}
			if left.f.Kind == "money" && right.f.Kind == "money" && left.end < right.start && strings.TrimSpace(s[left.end:right.start]) == "," && commaMoneyGroupRE.MatchString(s[right.start:right.end]) {
				return true
			}
		}
	}
	return slices.ContainsFunc(checks, func(c candidate) bool {
		return c.priority > 0 && (compoundPrefixWithRates(s, c.start, cs) || compoundSuffixBeforeDates(s, c.end, cs))
	})
}

var comparativeRE = regexp.MustCompile(`(?i)^(?:\s*(?:<=|>=|<|>|≤|≥)\s*|\s+(?:(?:less|more|greater)\s+than|after|before|prior\s+to)\s+)$`)

var commaMoneyGroupRE = regexp.MustCompile(`^[0-9]{3}(?:\.[0-9]+)?(?:\s|$)`)

// A slash rate requires a letter-bearing noun, not a bare numeric divisor.
var slashRateRE = regexp.MustCompile(`(?i)^\s*/\s*(?:((?:` + numRange + `))\s+)?(?:(?:business|working|calendar|free|paid|additional|extra)\s+)?([\p{L}][\p{L}\p{Nd}]*)`)

func slashRateEnd(s string) int {
	loc := slashRateRE.FindStringSubmatchIndex(s)
	if loc == nil || numeralWord(s[loc[4]:loc[5]]) {
		return 0
	}
	if loc[2] >= 0 {
		if _, _, ok := numbers(s[loc[2]:loc[3]], 1); !ok {
			return 0
		}
	}
	if loc[1] < len(s) {
		r, _ := utf8.DecodeRuneInString(s[loc[1]:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return 0
		}
	}
	return loc[1]
}

// A stated end date is not an unsupported numeric range endpoint.
func compoundSuffixBeforeDates(s string, end int, cs []candidate) bool {
	if slices.ContainsFunc(cs, func(c candidate) bool {
		return c.f.Kind == dateKind && c.start >= end && dateBoundConnectorRE.MatchString(s[end:c.start])
	}) {
		return false
	}
	if n := slashRateEnd(s[end:]); n > 0 {
		return compoundNumberSuffix(s[end+n:])
	}
	return compoundNumberSuffix(s[end:])
}

var dateBoundConnectorRE = regexp.MustCompile(`(?i)^\s+(?:until|till|through|to)\s+$`)

func endsTypedFigure(s string) bool {
	for _, re := range []*regexp.Regexp{moneyRE, unitRE, countRE} {
		matches := re.FindAllStringIndex(s, -1)
		if len(matches) > 0 && matches[len(matches)-1][1] == len(s) {
			return true
		}
	}
	return false
}

func numeralWord(s string) bool {
	s = strings.ToLower(s)
	switch s {
	case "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety", "hundred", "thousand", "million", "billion", "trillion":
		return true
	}
	_, ok := numberWords[s]
	return ok
}

func compoundNumberSuffix(suffix string) bool {
	r, _ := utf8.DecodeRuneInString(suffix)
	if unicode.IsLetter(r) || unicode.IsDigit(r) || groupedNumberSuffix(suffix) {
		return true
	}
	suffix = strings.TrimLeftFunc(suffix, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`()]"'‘’“”`, r) })
	r, _ = utf8.DecodeRuneInString(suffix)
	if numericOperator(r) {
		return true
	}
	joined := strings.HasPrefix(suffix, ",")
	suffix = strings.TrimLeftFunc(strings.TrimPrefix(suffix, ","), unicode.IsSpace)
	r, _ = utf8.DecodeRuneInString(suffix)
	if numericOperator(r) {
		return true
	}
	i := strings.IndexFunc(suffix, func(r rune) bool { return !unicode.IsLetter(r) })
	if i < 0 {
		i = len(suffix)
	}
	connector := strings.ToLower(suffix[:i])
	switch connector {
	case "or", "plus", "minus", "point", "off":
		return true
	case "less":
		suffix = strings.TrimLeftFunc(suffix[i:], unicode.IsSpace)
	case "and", "to", "through", "until", "till":
		joined = joined || connector == "and"
		suffix = strings.TrimLeftFunc(suffix[i:], func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`(["'‘’“”`, r) })
	}
	for _, re := range []*regexp.Regexp{moneyRE, unitRE, countRE} {
		if loc := re.FindStringIndex(suffix); loc != nil && loc[0] == 0 {
			// Only a conjunction or comma can introduce an independent fact.
			return !joined
		}
	}
	if connector == "and" || connector == "to" || connector == "through" || connector == "until" || connector == "till" {
		if approximationSuffixRE.MatchString(suffix) {
			return true
		}
		s := strings.ToLower(strings.Join(strings.Fields(suffix), " "))
		for _, q := range boundWords {
			for _, word := range q.words {
				if s == word || strings.HasPrefix(s, word+" ") {
					return true
				}
			}
		}
	}
	i = strings.IndexFunc(suffix, func(r rune) bool { return !unicode.IsLetter(r) })
	if i < 0 {
		i = len(suffix)
	}
	r, _ = utf8.DecodeRuneInString(suffix)
	return unicode.IsDigit(r) || numeralWord(suffix[:i]) || fractionSuffixRE.MatchString(suffix)
}

const closingPunctuation = ")]}\"'”’»"

func identifierEndsAt(suffix string) bool {
	return tokenEndsAt(suffix) && !identifierSuffixMaskRE.MatchString(suffix)
}

// A date or identifier must end at prose or detached sentence punctuation,
// not a continued token or further digit-bearing group after whitespace.
func tokenEndsAt(suffix string) bool {
	if suffix == "" {
		return true
	}
	r, n := utf8.DecodeRuneInString(suffix)
	if unicode.IsSpace(r) {
		fields := strings.Fields(suffix)
		return len(fields) == 0 || !strings.ContainsAny(fields[0], "0123456789")
	}
	if strings.ContainsRune(".,;:!?"+closingPunctuation, r) {
		next, _ := utf8.DecodeRuneInString(suffix[n:])
		return len(suffix) == n || unicode.IsSpace(next) || strings.ContainsRune(closingPunctuation, next)
	}
	return false
}

// A whitespace-separated typed figure may follow a complete date.
func dateEndsAt(suffix string) bool {
	if dateSlashSuffixRE.MatchString(suffix) {
		return false
	}
	if tokenEndsAt(suffix) {
		return true
	}
	if suffix == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(suffix)
	if !unicode.IsSpace(r) {
		return false
	}
	suffix = strings.TrimLeftFunc(suffix, unicode.IsSpace)
	for _, re := range []*regexp.Regexp{moneyRE, unitRE, countRE} {
		if loc := re.FindStringIndex(suffix); loc != nil && loc[0] == 0 {
			return true
		}
	}
	return false
}

func dateContinuedPrefix(prefix string) bool {
	prefix = strings.TrimRightFunc(prefix, unicode.IsSpace)
	r, n := utf8.DecodeLastRuneInString(prefix)
	if !numericOperator(r) {
		return false
	}
	previous, _ := utf8.DecodeLastRuneInString(strings.TrimRightFunc(prefix[:len(prefix)-n], unicode.IsSpace))
	return unicode.IsDigit(previous)
}

var dateSlashSuffixRE = regexp.MustCompile(`(?i)^\s*/\s*(?:[0-9]|january\b|february\b|march\b|april\b|may\b|june\b|july\b|august\b|september\b|october\b|november\b|december\b)`)

func numbers(s string, multiplier int64) (string, string, bool) {
	parts := wordRE.FindAllString(strings.ToLower(s), -1)
	if len(parts) < 1 || len(parts) > 2 {
		return "", "", false
	}
	vals := make([]string, 0, len(parts))
	var previous *big.Rat
	for _, p := range parts {
		if len(p) > 128 {
			return "", "", false
		}
		if v, ok := numberWords[p]; ok {
			p = v
		}
		p = strings.ReplaceAll(p, ",", "")
		r, ok := new(big.Rat).SetString(p)
		if !ok {
			return "", "", false
		}
		if previous != nil && previous.Cmp(r) > 0 {
			return "", "", false
		}
		previous = new(big.Rat).Set(r)
		r.Mul(r, big.NewRat(multiplier, 1))
		scale := 0
		if i := strings.IndexByte(p, '.'); i >= 0 {
			scale = len(p) - i - 1
		}
		v := r.FloatString(scale)
		if strings.Contains(v, ".") {
			v = strings.TrimRight(strings.TrimRight(v, "0"), ".")
		}
		vals = append(vals, v)
	}
	if len(vals) == 2 {
		return vals[0], vals[1], true
	}
	return vals[0], "", true
}
func parseMoney(s string, p []string) (Figure, bool) {
	var unit, num string
	if p[1] != "" {
		num = p[1]
		unit = strings.TrimSpace(strings.TrimSuffix(s, p[1]))
	} else {
		num = p[2]
		unit = p[3]
		if num == "" {
			num, unit = p[4], p[5]
		}
	}
	unit = strings.ToLower(unit)
	switch unit {
	case "dollars", "euros", "pounds", "roubles", "rubles":
		unit = strings.TrimSuffix(unit, "s")
	}
	if unit == "ruble" {
		unit = "rouble"
	}
	v, e, ok := numbers(num, 1)
	return Figure{Kind: "money", Unit: unit, Value: v, EndValue: e}, ok
}
func parseUnit(_ string, p []string) (Figure, bool) {
	num, unit := p[1], strings.ToLower(p[2])
	if num == "" {
		num, unit = p[3], p[4]
	}
	unit = strings.TrimPrefix(strings.Join(strings.Fields(unit), " "), "calendar ")
	kind, mul, canonical := "duration", int64(1), "minutes"
	switch {
	case unit == "%" || unit == "percent" || unit == "per cent":
		kind, canonical = "percent", "percent"
	case strings.HasPrefix(unit, "percentage"):
		kind, canonical = "percent", "percentage point"
	case strings.HasPrefix(unit, "business") || strings.HasPrefix(unit, "working"):
		canonical = "business days"
	case strings.HasPrefix(unit, "hour") || strings.HasPrefix(unit, "hr"):
		mul = 60
	case strings.HasPrefix(unit, "day"):
		mul = 1440
	case strings.HasPrefix(unit, "week"):
		mul = 10080
	case strings.HasPrefix(unit, "month"):
		canonical = "months"
	case strings.HasPrefix(unit, "year"):
		canonical = "months"
		mul = 12
	}
	v, e, ok := numbers(num, mul)
	return Figure{Kind: kind, Unit: canonical, Value: v, EndValue: e}, ok
}
func parseCount(_ string, p []string) (Figure, bool) {
	v, e, ok := numbers(p[1], 1)
	if strings.Contains(v, ".") || strings.Contains(e, ".") {
		ok = false
	}
	return Figure{Kind: "count", Unit: singular(strings.ToLower(p[3])), Value: v, EndValue: e, Modifier: strings.ToLower(p[2])}, ok
}
func singular(s string) string {
	if strings.HasSuffix(s, "es") && (strings.HasSuffix(s, "ches") || strings.HasSuffix(s, "sses")) {
		return s[:len(s)-2]
	}
	return strings.TrimSuffix(s, "s")
}
func parseIdentifier(_ string, p []string) (Figure, bool) {
	v := strings.ToLower(p[2])
	if strings.Contains(v, "*") || identifierMaskRE.MatchString(v) || !strings.ContainsAny(v, "0123456789") {
		return Figure{}, false
	}
	return Figure{Kind: "identifier", Unit: strings.ToLower(strings.Join(strings.Fields(p[1]), " ")), Value: strings.ReplaceAll(v, "-", "")}, true
}
func parseDate(s string, _ []string) (Figure, bool) {
	s = strings.ReplaceAll(s, ",", "")
	fields := strings.Fields(s)
	s = strings.Join(fields, " ")
	precision := "day"
	layouts := []string{"2006-01-02", "2 January 2006", "January 2 2006"}
	if len(fields) == 2 {
		layouts = []string{"January 2006"}
		precision = "month"
	}
	if strings.Contains(s, "/") {
		parts := strings.Split(s, "/")
		day, err := strconv.Atoi(parts[0])
		if err != nil || day <= 12 {
			return Figure{}, false
		}
		layouts = []string{"2/1/2006"}
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			value := t.Format("2006-01-02")
			if precision == "month" {
				value = t.Format("2006-01")
			}
			return Figure{Kind: dateKind, Unit: "date", Value: value, Precision: precision}, true
		}
	}
	return Figure{}, false
}
