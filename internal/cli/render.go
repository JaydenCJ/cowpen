// Small rendering helpers shared by the commands.
package cli

import "fmt"

// kindLetter maps a change kind to the single-letter prefix used by
// `cowpen status`, mirroring the letters agents already know from VCS.
func kindLetter(kind string) string {
	switch kind {
	case "added":
		return "A"
	case "modified":
		return "M"
	case "deleted":
		return "D"
	case "mode":
		return "X"
	case "type":
		return "T"
	}
	return "?"
}

// countNoun renders "1 pen" / "3 pens" — a real plural, never "(s)".
// The nouns cowpen prints all pluralize with a plain trailing "s".
func countNoun(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// humanBytes renders a byte count with a binary-ish, single-decimal unit.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
