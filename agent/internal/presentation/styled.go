package presentation

type StyledSpan struct {
	Text      string
	Role      Role
	Bold      bool
	Italic    bool
	Underline bool
}

type StyledLine []StyledSpan

func Text(value string) StyledSpan { return StyledSpan{Text: value, Role: RoleNormal} }

func RoleText(role Role, value string) StyledSpan { return StyledSpan{Text: value, Role: role} }

func Bold(role Role, value string) StyledSpan {
	return StyledSpan{Text: value, Role: role, Bold: true}
}
