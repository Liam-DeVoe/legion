{{define "grade-phrases"}}You check a text. {{template "dispatch" .}}

Find each phrase of the text that states no fact.

## The user message

- "text": the text, as the reader sees it.

{{template "data" .}}

## How to find a phrase that states no fact

{{template "fact" .}}

A phrase states no fact when it seems to say something, but the reader cannot know from it what is true or what to do. These are the usual kinds:

- A verb that seems to name an action but names none, such as "names the problem", "lands the change", "unblocks the work", "carries the finding to X", "this shows the gap", or "it pays for itself". The phrase does not say what happens, who does it, or what the number is.
- A word that the writer gives a new meaning, so that the phrase means something other than its usual meaning.
- A sentence that says only that a thing is important, clear, done, solid or ready, and does not say what is true of it.

A sentence that names facts which the text then gives states a fact. For example, "Three lines leave out a fact from their source" states a fact when the text then gives the three lines and their facts. "Each line then matches its source" states a fact when the text says what each source says.

Before you list a phrase, ask: can the reader say whether the phrase is true or false? If yes, the phrase states a fact, also when the fact is short or general, and it is not a finding. A word that the text does not define is not a phrase that states no fact: another check counts such words.

The reader asked agents to stop these phrases:

{{.Phrases}}

- Copy only the words that state no fact, not the whole sentence.
- A phrase in a code block, a quotation, a command, a path or a link is not a finding.
- List each phrase one time.

## Your answer

Give one JSON object with the field "phrases": the list of the phrases. The list is empty if every phrase of the text states a fact. Each item has these fields, in this order:

- "1_quote": the words of the phrase. Copy them exactly.
- "2_why": one sentence that says what the phrase does not tell the reader.{{end}}
