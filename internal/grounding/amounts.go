package grounding

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/currency"
)

var amountScales = map[string]int{"thousand": 3, "million": 6, "billion": 9, "k": 3, "m": 6, "bn": 9}

// Compatibility is symmetric but deliberately not transitive: USD and CAD
// each match a bare dollar symbol, but do not match each other.
var symbolCurrencies = map[string]string{
	"$": " AUD BBD BMD BND BSD BZD CAD FJD GYD HKD JMD KYD LRD NAD NZD SBD SGD SRD TTD TWD USD XCD ZWD ZWL ",
	"¥": " JPY CNY ", "£": " GBP ", "€": " EUR ",
}

func compatibleCurrency(a, b string) bool {
	return a == b || strings.Contains(symbolCurrencies[a], " "+b+" ") || strings.Contains(symbolCurrencies[b], " "+a+" ")
}
func currencyAt(rs []rune, start int) (int, string) {
	if start >= len(rs) {
		return start, ""
	}
	for _, p := range []struct{ prefix, code string }{{"US$", "USD"}, {"C$", "CAD"}, {"A$", "AUD"}} {
		end := start + len(p.prefix)
		if end <= len(rs) && strings.EqualFold(string(rs[start:end]), p.prefix) {
			return end, p.code
		}
	}
	if unicode.Is(unicode.Sc, rs[start]) {
		return start + 1, string(rs[start])
	}
	if start+3 <= len(rs) {
		code := string(rs[start : start+3])
		if _, err := currency.ParseISO(code); err == nil && code == strings.ToUpper(code) && (start+3 == len(rs) || (!wordRune(rs[start+3]) || digit(rs[start+3]))) {
			return start + 3, code
		}
	}
	return start, ""
}
func amountSpace(rs []rune, i int) int {
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t' || rs[i] == '\u00a0' || rs[i] == '\u202f') {
		i++
	}
	return i
}
func amountAt(rs []rune, start int) (int, []string) {
	if start > 0 && (wordRune(rs[start-1]) || strings.ContainsRune("-/_", rs[start-1])) {
		return 0, nil
	}
	// Recover before a maximal identifier or sign, while skipping any earlier
	// currency-only tokens. Retrying the same long symbol/code run is quadratic.
	i := start
	negative, signed := false, false
	classes := []string{}
	prefixEnd, identifierStart, signStart := 0, -1, -1
	failed := func() (int, []string) {
		safe := prefixEnd
		if identifierStart >= start && identifierStart < safe {
			safe = identifierStart
		}
		if signStart >= start && signStart < safe {
			safe = signStart
		}
		if safe <= start {
			return 0, nil
		}
		return safe, nil
	}
	for i < len(rs) {
		if rs[i] == '-' || rs[i] == '+' {
			if signed {
				return failed()
			}
			negative, signed = rs[i] == '-', true
			signStart = i
			i = amountSpace(rs, i+1)
			if len(classes) > 0 {
				prefixEnd = i
			}
			continue
		}
		end, code := currencyAt(rs, i)
		if code == "" {
			break
		}
		// A joined ISO token belongs to the identifier scanner if the amount
		// fails. Inspect only its boundary; probing the remaining token at each
		// currency code would make USD.USD... quadratic.
		if identifierStart < 0 && end-i == 3 && isASCIIAlpha(rs[end-1]) && end < len(rs) && idChar(rs[end]) {
			identifierStart = i
		}
		classes = append(classes, code)
		i = amountSpace(rs, end)
		prefixEnd = i
	}
	numberStart := i
	if i >= len(rs) || (!digit(rs[i]) && (rs[i] != '.' || i+1 >= len(rs) || !digit(rs[i+1]))) {
		return failed()
	}
	for i < len(rs) && (digit(rs[i]) || separator(rs[i])) {
		i++
	}
	// Sentence punctuation and whitespace after the number are not part of it.
	for i > numberStart && !digit(rs[i-1]) {
		if i-numberStart >= 2 && strings.ContainsRune(",.'", rs[i-1]) && strings.ContainsRune(",.'", rs[i-2]) {
			return failed()
		}
		i--
	}
	numberEnd := i
	scaleStart := amountSpace(rs, i)
	scaleEnd := scaleStart
	for scaleEnd < len(rs) && isASCIIAlpha(rs[scaleEnd]) {
		scaleEnd++
	}
	power := amountScales[strings.ToLower(string(rs[scaleStart:scaleEnd]))]
	if power > 0 {
		i = scaleEnd
	}
	for {
		start := amountSpace(rs, i)
		end, code := currencyAt(rs, start)
		if code == "" || (end-start == 3 && isASCIIAlpha(rs[end-1]) && string(rs[start:end]) != strings.ToUpper(string(rs[start:end]))) {
			break
		}
		// A marker followed by another numeric value starts the next amount.
		next := amountSpace(rs, end)
		if next < len(rs) && (rs[next] == '+' || rs[next] == '-') {
			next = amountSpace(rs, next+1)
		}
		if next < len(rs) && (digit(rs[next]) || rs[next] == '.' && next+1 < len(rs) && digit(rs[next+1])) {
			break
		}
		classes = append(classes, code)
		i = end
	}
	if len(classes) == 0 {
		return failed()
	}
	code := amountCurrencyClass(classes)
	if !figureBoundary(rs, i) && (power <= 0 || i >= len(rs) || rs[i] != ',' || i+1 >= len(rs) || !digit(rs[i+1])) {
		return failed()
	}
	values := numberValues(string(rs[numberStart:numberEnd]))
	if len(values) == 0 {
		return failed()
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = scaleDecimal(value, power, negative)
		result = append(result, code+" "+value)
	}
	return i, result
}

// Move the decimal point using digits, never a floating-point conversion.
func scaleDecimal(value string, power int, negative bool) string {
	parts := strings.SplitN(value, ".", 2)
	integer, fraction := parts[0], ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if power > 0 {
		if len(fraction) < power {
			fraction += strings.Repeat("0", power-len(fraction))
		}
		integer += fraction[:power]
		fraction = fraction[power:]
	}
	return canonical(integer, fraction, negative)
}

func amountParts(value string) (string, string) {
	code, number, _ := strings.Cut(value, " ")
	return code, number
}

// Multiple compatible markers narrow a symbol to its explicit ISO currency.
// Conflicting combinations remain one class instead of discarding a marker.
func amountCurrencyClass(classes []string) string {
	slices.Sort(classes)
	classes = slices.Compact(classes)
	for _, a := range classes {
		for _, b := range classes {
			if !compatibleCurrency(a, b) {
				return strings.Join(classes, "+")
			}
		}
	}
	for _, class := range classes {
		if len(class) == 3 && isASCIIAlpha(rune(class[0])) {
			return class
		}
	}
	return classes[0]
}
