{{define "reader-own-words"}}{{$t := "text"}}{{if .FirstParagraph}}{{$t = "paragraph"}}{{end}}## The reader's own words

The user message also holds "reader_own_words": words of the {{$t}} that the reader used himself, in a text of his own, before the agent wrote the text. Each word is in lower case.

The reader knows these words. Do not count them as words that the {{$t}} does not explain: do not name them in the sentence that names each name or word that the {{$t}} does not explain. They are not part of the {{$t}}: answer from the words of the {{$t}} alone.{{end}}
