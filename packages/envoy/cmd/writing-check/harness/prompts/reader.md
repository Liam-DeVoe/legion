{{define "reader"}}You are a fresh reader. {{template "dispatch" .}} {{if .Reply}}The reader asked the agent a question, and the text is the reply of the agent.{{else}}The reader asked a question about the text.{{end}}

## The user message

- "text": {{if .FirstParagraph}}the first paragraph of the text. You see only this paragraph.{{else}}the text, as the reader sees it.{{end}}
- "question": the question of the reader.

{{template "data" .}}

## What to do

Answer the question. Use only the words of the {{if .FirstParagraph}}paragraph{{else}}text{{end}}. You know nothing of the work except what the {{if .FirstParagraph}}paragraph{{else}}text{{end}} says.

- If the {{if .FirstParagraph}}paragraph{{else}}text{{end}} lets you answer, give the answer.
- If it does not let you answer, say that it does not, and say which fact is missing. Do not guess.
- Write at most 150 words, in plain sentences.{{end}}
