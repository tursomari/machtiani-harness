A patch was just applied. Examine the diff to ensure the change was as intended and that you did not reduplicate or unnecessarily delete anything outside of your intention -- reject if you spot mistakes or risky alterations.
Accept keeps the changes. Reject applies the undo patch to revert them.
Begin your reply immediately with `Decision:`—no leading commentary.
Allowed values: accept or reject (case-insensitive).
Always include a second line formatted `Reason: <brief justification>` that cites why the changes are correct and safe (even if you accept).

{{- if .HasPending }}
{{- if .Pending.HasDescription }}Patch summary: {{.Pending.Description}}
{{- end }}
{{- if gt .Pending.Sequence 0 }}Patch sequence: {{.Pending.Sequence}}
{{- end }}
{{- if .Pending.HasDiffStats }}Diff stats: +{{.Pending.Insertions}} / -{{.Pending.Deletions}}
{{- end }}
{{- if .Pending.HasFiles }}Files modified:
{{- range .Pending.Files }}- {{.}}
{{- end }}
{{- end }}
{{- if .Pending.PatchPath }}Patch file: {{.Pending.PatchPath}}
{{- if .Pending.HasDiffPreview }}
Patch diff preview:
```diff
{{.Pending.DiffPreview}}
```
{{- end }}
{{- end }}
{{- if .Pending.UndoPatchPath }}Undo patch file: {{.Pending.UndoPatchPath}}
{{- end }}
Use the transcript diff above and any diagnostics to inform your choice.

{{- end }}
{{- if .SuccessFiles }}
Files already accepted earlier this session (prefer new work unless necessary):
{{- range .SuccessFiles }}- {{.}}
{{- end }}
{{- if gt .SuccessOverflow 0 }}- … ({{.SuccessOverflow}} more)
{{- end }}

{{- end }}
{{- if gt .AppliedPatches 0 }}
Strict patch successes so far: {{.AppliedPatches}}. Accepting keeps them; rejecting reverts the latest patch only.

{{- end }}
Step {{.Step}} of {{.MaxSteps}}. Decide.
