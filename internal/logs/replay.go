package logs

import "strings"

// replaySuffix closes a published line that the live-tail publisher replays
// rather than publishes as it arrives. The stored encoding (EncodeLine) always
// ends with the closing quote of its "msg" string, so a stored line never ends
// with this suffix, and DecodeLine ignores the unknown field, so a follower that
// predates the flag shows the line unchanged.
const replaySuffix = `,"replay":true}`

// MarkReplay flags an encoded line as a replay for the live tail. A line that is
// not a JSON object (a legacy plain line) is returned unchanged.
func MarkReplay(line string) string {
	if len(line) < 2 || line[0] != '{' || line[len(line)-1] != '}' {
		return line
	}
	return line[:len(line)-1] + replaySuffix
}

// SplitReplay reports whether a published line was flagged by MarkReplay and
// returns it with the flag removed, which is its stored encoding.
func SplitReplay(line string) (string, bool) {
	if !strings.HasSuffix(line, replaySuffix) {
		return line, false
	}
	return line[:len(line)-len(replaySuffix)] + "}", true
}
