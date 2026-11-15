{{define "grade-undefined"}}You check a text. {{template "dispatch" .}} The reader knows the work in general, but he did not see what the agent did.

Find each word or name of the text that the reader cannot understand from the text and the record.

## The user message

- "text": the text, as the reader sees it.
{{template "record-tag" .}}

{{template "data" .}}

## How to find an undefined word or name

A word or a name is undefined only when all of these are true:

- The text uses it.
- The text does not say what it means or what it is.
- The record does not say what it means or what it is.
- It is not a word of common English, and not a common word of software work (such as session, pull request, review, test, branch, commit, deploy, key, server or login), used in its common meaning.

A word or a name is defined when one of these is true:

- The text says what it means or what it is, in the same sentence or in another sentence.
- The record says what it means or what it is: in the title of the issue, in a document, or in a text of the thread.

These are the usual kinds of undefined words:

- A term that the writer made up, or a common word that the writer uses with a new meaning.
- A code name, a short form or an abbreviation.
- The name of a file, a function, a setting, a tool, a part of a system, a task or a document, when neither the text nor the record says what it is or what it does.

These are not findings:

- A word that is vague, or that does not say which thing it is. A vague word is not an undefined word.
- A link, an issue key (such as ABC-123), the name of a person, a number, a date or a time.
- A word in a code block, a command or a path that the text gives only for the reader to copy.

List each word or name one time. If no word or name is undefined, give an empty list. An empty list is a correct answer.

## Your answer

Give one JSON object with the field "undefined": the list of the undefined words and names. Each item has these fields, in this order:

- "1_word": the word or the name. Copy it exactly.
- "2_defined_in_text": true if the text says what it means or what it is, else false.
- "3_defined_in_record": true if the record says what it means or what it is, else false.
- "4_common_meaning": true if it is a word of common English or a common word of software work, used in its common meaning, else false.
- "5_why": one sentence that says what the reader cannot know about it.

Do not list a word for which "2_defined_in_text", "3_defined_in_record" or "4_common_meaning" is true.{{end}}
