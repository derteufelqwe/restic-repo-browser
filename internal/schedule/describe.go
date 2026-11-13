package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

var monthNames = [...]string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

var shortNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

// Describe explains the expression in plain English, for example
// "Every Sunday at 04:00" or "Every 15 minutes, between 09:00 and 17:59, Monday through Friday".
func (s Schedule) Describe() string {
	f := strings.Fields(s.Spec)
	if len(f) != 5 {
		return "Custom schedule"
	}
	if d := describeCommon(f); d != "" {
		return d
	}
	if d, ok := describeGeneral(f); ok {
		return d
	}
	return "Custom schedule"
}

// describeCommon phrases the shapes people use most.
func describeCommon(f []string) string {
	m, h, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	clock := ""
	if isNum(m) && isNum(h) {
		clock = fmt.Sprintf("%02s:%02s", h, m)
	}
	switch {
	case clock != "" && dom == "*" && mon == "*" && dow == "*":
		return "Every day at " + clock
	case clock != "" && dom == "*" && mon == "*" && isNum(dow):
		return fmt.Sprintf("Every %s at %s", weekdayName(atoi(dow)), clock)
	case clock != "" && isNum(dom) && mon == "*" && dow == "*":
		return fmt.Sprintf("On day %s of every month at %s", dom, clock)
	case clock != "" && isNum(dom) && strings.HasPrefix(mon, "*/") && isNum(mon[2:]) && dow == "*":
		return fmt.Sprintf("On day %s, every %s months, at %s", dom, mon[2:], clock)
	case isNum(m) && h == "*" && dom == "*" && mon == "*" && dow == "*":
		return fmt.Sprintf("Every hour at %02s minutes past", m)
	case strings.HasPrefix(m, "*/") && isNum(m[2:]) && h == "*" && dom == "*" && mon == "*" && dow == "*":
		return fmt.Sprintf("Every %s minutes", m[2:])
	case m == "*" && h == "*" && dom == "*" && mon == "*" && dow == "*":
		return "Every minute"
	}
	return ""
}

// describeGeneral composes a sentence from the five fields.
func describeGeneral(f []string) (string, bool) {
	m, h, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	var parts []string

	time, ok := describeTime(m, h)
	if !ok {
		return "", false
	}
	parts = append(parts, time)

	var dayParts []string
	if dom != "*" {
		p, ok := describeSet(dom, func(n int) string { return strconv.Itoa(n) }, "day", "days", " of the month")
		if !ok {
			return "", false
		}
		dayParts = append(dayParts, p)
	}
	if dow != "*" {
		p, ok := describeSet(dow, weekdayName, "", "", "")
		if !ok {
			return "", false
		}
		dayParts = append(dayParts, p)
	}
	if len(dayParts) > 0 {
		// When both are restricted cron runs on days matching either.
		parts = append(parts, "on "+strings.Join(dayParts, " or on "))
	}
	if mon != "*" {
		if strings.HasPrefix(mon, "*/") && isNum(mon[2:]) {
			parts = append(parts, "every "+mon[2:]+" months")
		} else {
			p, ok := describeSet(mon, func(n int) string { return monthNames[n%13] }, "", "", "")
			if !ok {
				return "", false
			}
			parts = append(parts, "in "+p)
		}
	}
	return strings.Join(parts, ", "), true
}

func describeTime(m, h string) (string, bool) {
	// A handful of fixed times: "At 03:30" or "At 03:00 and 15:00".
	if mins, ok := numbers(m); ok {
		if hours, ok := numbers(h); ok && len(mins)*len(hours) <= 6 {
			var times []string
			for _, hh := range hours {
				for _, mm := range mins {
					times = append(times, fmt.Sprintf("%02d:%02d", hh, mm))
				}
			}
			return "At " + joinAnd(times), true
		}
	}
	var mp string
	switch {
	case m == "*":
		mp = "Every minute"
	case strings.HasPrefix(m, "*/") && isNum(m[2:]):
		mp = "Every " + m[2:] + " minutes"
	default:
		p, ok := describeSet(m, strconv.Itoa, "minute", "minutes", "")
		if !ok {
			return "", false
		}
		mp = "At " + p
	}
	switch {
	case h == "*":
		if m != "*" && !strings.HasPrefix(m, "*/") {
			mp += " past every hour"
		}
		return mp, true
	case strings.HasPrefix(h, "*/") && isNum(h[2:]):
		return mp + " past every " + ordinal(h[2:]) + " hour", true
	}
	if lo, hi, ok := rangeOf(h); ok {
		return fmt.Sprintf("%s, between %02d:00 and %02d:59", mp, lo, hi), true
	}
	p, ok := describeSet(h, strconv.Itoa, "hour", "hours", "")
	if !ok {
		return "", false
	}
	return mp + " past " + p, true
}

// describeSet phrases a comma list of numbers, ranges and steps. noun/plural
// ("day", "days") prefix plain numbers; suffix follows the whole phrase.
func describeSet(spec string, name func(int) string, noun, plural, suffix string) (string, bool) {
	var items []string
	count := 0
	for _, part := range strings.Split(spec, ",") {
		step := ""
		if base, st, found := strings.Cut(part, "/"); found {
			if !isNum(st) {
				return "", false
			}
			part, step = base, st
		}
		switch {
		case part == "*":
			if step == "" {
				return "", false
			}
			items = append(items, "every "+ordinal(step)+" "+firstNonEmpty(noun, "value"))
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			na, ok1 := resolve(a)
			nb, ok2 := resolve(b)
			if !ok1 || !ok2 {
				return "", false
			}
			phrase := name(na) + " through " + name(nb)
			if step != "" {
				phrase = "every " + ordinal(step) + " from " + phrase
			}
			items = append(items, phrase)
			count += 2
		default:
			n, ok := resolve(part)
			if !ok {
				return "", false
			}
			items = append(items, name(n))
			count++
		}
	}
	out := joinAnd(items)
	if noun != "" && len(items) > 0 && !strings.HasPrefix(items[0], "every") {
		if count > 1 {
			out = plural + " " + out
		} else {
			out = noun + " " + out
		}
	}
	return out + suffix, true
}

func resolve(tok string) (int, bool) {
	if n, ok := shortNames[strings.ToLower(tok)]; ok {
		return n, true
	}
	n, err := strconv.Atoi(tok)
	return n, err == nil
}

func rangeOf(spec string) (int, int, bool) {
	a, b, found := strings.Cut(spec, "-")
	if !found || strings.ContainsAny(spec, ",/") {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(a)
	hi, err2 := strconv.Atoi(b)
	return lo, hi, err1 == nil && err2 == nil
}

// numbers parses "5" or "0,30" into numbers; false for ranges, steps and "*".
func numbers(spec string) ([]int, bool) {
	var out []int
	for _, p := range strings.Split(spec, ",") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func weekdayName(n int) string { return weekday[n%7] }

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func ordinal(n string) string {
	switch n {
	case "1":
		return n
	case "2":
		return "2nd"
	case "3":
		return "3rd"
	}
	return n + "th"
}

func isNum(s string) bool { return numRe.MatchString(s) }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
