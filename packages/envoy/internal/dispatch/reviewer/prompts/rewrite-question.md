{{define "rewrite-question"}}A person asked Dispatch to write your {{.Kind}} {{.ID}} again, so that they can {{if .Ask}}answer it{{else}}understand it{{end}} without your conversation. Dispatch writes the new text from its record and from your answer to this question.

What must the reader know to {{if .Ask}}answer the ask{{else}}understand the text{{end}}? Give these facts in plain words:
{{- if .Ask}}

1. The decision that the reader must make.
2. Why the decision is necessary now.
3. The facts that the reader must know to make the decision.
4. Your recommendation, with its reason.
5. What each option causes.
{{- end}}

If you use a word or a name that the reader cannot know, say what it means. Give only facts that you know. Do not change the {{.Kind}} yourself.

The {{.Kind}}:

{{.Text}}
{{end}}
