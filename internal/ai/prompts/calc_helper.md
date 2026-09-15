The operator asked: "{{.Question}}"

The arithmetic has already been computed. Do not recompute it and do not
introduce numbers that are not listed here.

{{if .Inputs}}Inputs:
{{range .Inputs}}- {{.Label}}: {{.Value}}
{{end}}{{end}}
{{if .Results}}Computed results:
{{range .Results}}- {{.Label}}: {{.Value}}
{{end}}{{end}}
{{if .Warnings}}Warnings:
{{range .Warnings}}- {{.}}
{{end}}{{end}}

Explain these results to the operator in at most four sentences. State what the
numbers mean for the position they described, and flag any warning above.

Report every figure exactly as given. If a figure needed for the question is
missing, say which one rather than estimating it. No recommendation about
whether to take the trade.
