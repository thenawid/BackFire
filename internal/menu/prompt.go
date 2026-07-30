package menu

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ANSI styling for the console. The palette matches the web panel — blue as the
// accent, cyan for secondary detail — so the two feel like one product.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	blue   = "\033[38;5;39m"
	cyan   = "\033[38;5;51m"
	green  = "\033[38;5;41m"
	yellow = "\033[38;5;214m"
	red    = "\033[38;5;203m"
	grey   = "\033[38;5;245m"
)

var in = bufio.NewReader(os.Stdin)

// Every prompt shows the value that will be used if the operator simply presses
// Enter, in parentheses. Nothing is ever silently assumed: if a question has a
// sensible default, that default is on screen before it is accepted.

// ask reads a line, returning def when the operator just presses Enter.
func ask(label, def string) string {
	if def != "" {
		fmt.Printf("  %s%s%s %s(default:%s %s%s%s)%s: ",
			bold, label, reset, grey, reset, cyan, def, grey, reset)
	} else {
		fmt.Printf("  %s%s%s %s(required)%s: ", bold, label, reset, grey, reset)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// askInt reads a whole number, keeping def on an empty or unparseable answer.
func askInt(label string, def int) int {
	for {
		s := ask(label, strconv.Itoa(def))
		n, err := strconv.Atoi(s)
		if err == nil {
			return n
		}
		warn("“%s” is not a number — enter a number, or press Enter for %d.", s, def)
	}
}

// askYesNo reads a yes/no answer, with the default spelled out.
func askYesNo(label string, def bool) bool {
	d := "no"
	if def {
		d = "yes"
	}
	for {
		switch strings.ToLower(ask(label+" (yes/no)", d)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			warn("Answer yes or no — or press Enter for %s.", d)
		}
	}
}

// askChoice presents a numbered list and accepts either the number or the value.
// Returns the index chosen.
func askChoice(label string, options []string, notes []string, defIndex int) int {
	fmt.Println()
	for i := range options {
		marker := " "
		if i == defIndex {
			marker = blue + "▸" + reset
		}
		note := ""
		if i < len(notes) {
			note = grey + notes[i] + reset
		}
		fmt.Printf("   %s %s%2d)%s %s%-9s%s %s\n", marker, dim, i+1, reset, cyan, options[i], reset, note)
	}
	fmt.Println()
	for {
		s := ask(label, options[defIndex])
		if n, err := strconv.Atoi(s); err == nil {
			if n >= 1 && n <= len(options) {
				return n - 1
			}
			warn("Pick a number between 1 and %d.", len(options))
			continue
		}
		for i, o := range options {
			if strings.EqualFold(o, s) {
				return i
			}
		}
		warn("“%s” is not one of the options.", s)
	}
}

// --- output helpers ---------------------------------------------------------

func title(s string) {
	fmt.Printf("\n%s%s%s\n", bold+blue, s, reset)
	fmt.Printf("%s%s%s\n", dim, strings.Repeat("─", len([]rune(s))+2), reset)
}

func note(format string, args ...any) {
	fmt.Printf("  %s%s%s\n", grey, fmt.Sprintf(format, args...), reset)
}
func ok(format string, args ...any) {
	fmt.Printf("  %s✓%s %s\n", green, reset, fmt.Sprintf(format, args...))
}
func warn(format string, args ...any) {
	fmt.Printf("  %s!%s %s\n", yellow, reset, fmt.Sprintf(format, args...))
}
func fail(format string, args ...any) {
	fmt.Printf("  %s✗%s %s\n", red, reset, fmt.Sprintf(format, args...))
}
func field(k string, v any) { fmt.Printf("  %s%-16s%s %v\n", grey, k, reset, v) }

// pause waits for Enter so a result is readable before the menu redraws.
func pause() {
	fmt.Printf("\n  %spress Enter to continue%s", dim, reset)
	_, _ = in.ReadString('\n')
}

// statusColor renders a systemd state in the colour that matches its meaning.
func statusColor(s string) string {
	switch s {
	case "active", "running":
		return green + s + reset
	case "inactive", "failed":
		return red + s + reset
	default:
		return yellow + s + reset
	}
}
