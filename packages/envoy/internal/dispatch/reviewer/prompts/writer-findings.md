{{define "writer-findings"}}
{{- if .Block -}}
Dispatch keeps your {{.Kind}} {{.ID}} out of its reader's Inbox until you act on it. It shows in its document. A reviewer read it and found {{len .Findings}} {{if eq (len .Findings) 1}}problem{{else}}problems{{end}} for its reader.
{{- else if .Held -}}
Dispatch holds your {{.Kind}} {{.ID}}. No person can see it until you act on it. A reviewer read it and found {{len .Findings}} {{if eq (len .Findings) 1}}problem{{else}}problems{{end}} for its reader.
{{- else -}}
Dispatch posted your {{.Kind}} {{.ID}}. A reviewer read it after the post and found {{len .Findings}} {{if eq (len .Findings) 1}}problem{{else}}problems{{end}} for its reader.
{{- end}}

## The findings
{{range $index, $finding := .Findings}}
{{add $index 1}}. Rule set "{{$finding.RuleSet}}". The words: "{{$finding.Quote}}"
   Why: {{$finding.Why}}
   Fix: {{$finding.Fix}}
{{end}}
{{- if .Rewrite}}
## The rewrite from the reviewer

{{.Rewrite}}
{{- with .NotApplied}}

Dispatch cannot post this rewrite as it is: {{.}}.
{{- end}}
{{end}}
## What to do now
{{if .Held}}
You have {{.TimeLeft}}. Do one of these two things:

- To change the text, run `dispatch held-edit {{.ID}}`. The reviewer reads the changed text again.
- To post the text as you wrote it, run `dispatch held-post {{.ID}} --reason '<reason>'`. In the reason, say why the text is correct for its reader. Dispatch records the reason.

If you do neither in {{.TimeLeft}}, Dispatch posts {{if .PostsRewrite}}the rewrite from the reviewer, marked as rewritten{{else}}your text as you wrote it{{end}}.
{{- else}}
If a finding is correct, change the text{{with .EditCommand}}: run `{{.}}`{{end}}.
{{- end}}
{{end}}
