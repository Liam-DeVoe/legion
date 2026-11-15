{{define "grade-lost"}}You compare two texts. {{template "dispatch" .}} The original text is the text that the agent wrote. The new text is the text that the reader sees after a change to the original text.

Find each fact of the original text that the new text lost.

## The user message

- "original_text": the original text.
- "new_text": the new text.
{{- template "question-tag" .}}
{{- template "context-tags" .}}

{{template "data" .}}

## How to find a lost fact

{{template "fact" .}}

- A fact is lost when the original text states it and the new text does not state it.
- A fact that the new text states in other words is not lost.
- A fact that the new text states in a link, a reference or a list is not lost.
- Do not list a phrase of the original text that states no fact.
- List each lost fact one time.

## Your answer

Give one JSON object with the field "lost": the list of the lost facts. The list is empty if the new text lost no fact. Each item has these fields, in this order:

- "1_quote": the words of the original text that state the fact. Copy them exactly.
- "2_fact": the fact, in one sentence.{{end}}
