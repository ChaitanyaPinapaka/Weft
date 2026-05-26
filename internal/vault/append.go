package vault

import (
	"errors"
	"fmt"
	"html"
	"os"
	"strings"
	"time"
)

// ErrEmptyCapture is returned when AppendCapture is called with empty text.
// Capture is for thoughts; empty strings are noise, not signal.
var ErrEmptyCapture = errors.New("capture text is empty")

// AppendCapture appends a single <aside class="capture"> block to today's
// daily note, creating the note if it does not already exist. The text is
// HTML-escaped. Returns the vault-relative path of the daily note.
//
// The timestamp t supplies both the daily-note date (in t's location) and the
// display time inside <time>. The data-ts attribute is recorded in UTC RFC3339.
func (v *Vault) AppendCapture(t time.Time, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyCapture
	}

	rel, err := v.EnsureDaily(t)
	if err != nil {
		return "", err
	}

	snippet := fmt.Sprintf(
		"<aside class=\"capture\" data-ts=\"%s\"><time>%s</time> %s</aside>\n",
		t.UTC().Format(time.RFC3339),
		t.Format("15:04"),
		html.EscapeString(text),
	)

	f, err := os.OpenFile(v.abs(rel), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(snippet); err != nil {
		return "", err
	}
	return rel, nil
}
