package eval

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// answer.go reads an answer task's answer for what no judge is asked: the items it links and the
// numbers of its chart's table. An answer is the prose of its replies and the blocks it drew
// (going.BlockData), each block's rows the values the server's chain filled it with.

// MissingLinks is each item of links (Seed.Refs) that the answer does not link: an issue by its
// key, a document, a spec or a session by its reference, and a comment, an ask or a message by
// the id Dispatch stores it under (ItemID), in the prose or in a block's rows.
func MissingLinks(links []string, answer string, blocks []json.RawMessage) []string {
	var text strings.Builder
	text.WriteString(answer)
	for _, block := range blocks {
		text.WriteString("\n")
		text.Write(block)
	}
	haystack := text.String()
	var missing []string
	for _, link := range links {
		if !linked(haystack, link) {
			missing = append(missing, link)
		}
	}
	return missing
}

// linked reports whether text links the item ref names.
func linked(text, ref string) bool {
	lower := strings.ToLower(text)
	for _, kind := range []string{"ask", "comment", "message"} {
		if owner, id, found := strings.Cut(ref, "/"+kind+"/"); found {
			stored := ItemID(kind, id)
			return strings.Contains(lower, stored) || strings.Contains(lower, strings.ToLower(owner+"/"+kind+"/"+id))
		}
	}
	if strings.Contains(ref, "/") {
		return strings.Contains(lower, strings.ToLower(ref))
	}
	return regexp.MustCompile(`(?i)(^|[^a-z0-9-])` + regexp.QuoteMeta(ref) + `($|[^0-9])`).MatchString(text)
}

// MissingChartRows is each row of the chart's table that the answer does not show: no row of a
// block it drew holds the label and the count, and no line of its prose holds the label with the
// count beside it.
func MissingChartRows(rows []ChartRow, answer string, blocks []json.RawMessage) []string {
	var drawn []map[string]any
	for _, block := range blocks {
		var data struct {
			Rows []map[string]any `json:"rows"`
		}
		if json.Unmarshal(block, &data) == nil {
			drawn = append(drawn, data.Rows...)
		}
	}
	lines := strings.Split(strings.ToLower(answer), "\n")
	var missing []string
	for _, row := range rows {
		if !inBlocks(drawn, row) && !inLines(lines, row) {
			missing = append(missing, fmt.Sprintf("%s=%d", row.Label, row.Count))
		}
	}
	return missing
}

// inBlocks reports whether a drawn row holds the label as one of its values and the count as
// another.
func inBlocks(drawn []map[string]any, row ChartRow) bool {
	for _, values := range drawn {
		label, count := false, false
		for _, value := range values {
			switch v := value.(type) {
			case string:
				label = label || strings.EqualFold(strings.TrimSpace(v), row.Label)
			case float64:
				count = count || v == float64(row.Count)
			}
		}
		if label && count {
			return true
		}
	}
	return false
}

// inLines reports whether a line of the prose holds the label and, once the label is taken out,
// the count as a number of its own.
func inLines(lines []string, row ChartRow) bool {
	label := strings.ToLower(row.Label)
	count := regexp.MustCompile(`(^|[^0-9.])` + strconv.Itoa(row.Count) + `($|[^0-9.]|\.[^0-9])`)
	for _, line := range lines {
		i := strings.Index(line, label)
		if i < 0 {
			continue
		}
		if count.MatchString(line[:i] + " " + line[i+len(label):]) {
			return true
		}
	}
	return false
}
