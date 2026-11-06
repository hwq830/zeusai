package workflows

import (
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

var keyFigureValueRe = regexp.MustCompile(`^[0-9][0-9,]*(?:\.[0-9]+)?$`)

func sanitizeKeyFigures(taskID uuid.UUID, markdown string, in []keyFigure) []keyFigure {
	if len(in) > 4 {
		in = in[:4]
	}
	out := make([]keyFigure, 0, len(in))
	for _, figure := range in {
		figure.Label = strings.TrimSpace(figure.Label)
		figure.Value = strings.TrimSpace(figure.Value)
		figure.Unit = strings.TrimSpace(figure.Unit)
		reason := ""
		if figure.Label == "" || figure.Unit == "" || utf8.RuneCountInString(figure.Label) > 12 || !keyFigureValueRe.MatchString(figure.Value) {
			reason = "invalid_shape"
		} else if !keyFigureValueInBody(markdown, figure.Value) {
			reason = "not_in_body"
		}
		if reason != "" {
			slog.Info("report key figure dropped", "task_id", taskID, "label", figure.Label, "reason", reason)
			continue
		}
		if figure.Cite < 0 {
			figure.Cite = 0
		}
		out = append(out, figure)
	}
	return out
}

func keyFigureValueInBody(markdown, value string) bool {
	needle := strings.ReplaceAll(value, ",", "")
	if needle == "" {
		return false
	}
	haystack := strings.ReplaceAll(markdown, ",", "")
	for offset := 0; offset < len(haystack); {
		i := strings.Index(haystack[offset:], needle)
		if i < 0 {
			return false
		}
		i += offset
		end := i + len(needle)
		beforeOK := i == 0 || !isNumberByte(haystack[i-1])
		afterOK := end == len(haystack) || !isNumberByte(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		offset = i + 1
	}
	return false
}

func isNumberByte(b byte) bool {
	return (b >= '0' && b <= '9') || b == '.'
}

func toTemplateKeyFigures(in []keyFigure) []reporttemplate.KeyFigure {
	out := make([]reporttemplate.KeyFigure, 0, len(in))
	for _, figure := range in {
		out = append(out, reporttemplate.KeyFigure{Label: figure.Label, Value: figure.Value, Unit: figure.Unit, Cite: figure.Cite})
	}
	return out
}
