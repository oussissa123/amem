package chunk

import "strings"

// EmbedInput is the text sent to the embeddings provider for a chunk: the
// chunk prefixed by "<type> <key> — <title> › <section path>" (spec 02 §6.1).
func EmbedInput(typ, key, title, section, text string) string {
	var b strings.Builder
	b.WriteString(typ + " " + key + " — " + title)
	if section != "" && section != "header" {
		b.WriteString(" › " + section)
	}
	b.WriteByte('\n')
	b.WriteString(text)
	return b.String()
}
