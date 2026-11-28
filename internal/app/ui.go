package app

//go:generate go run ./zonegen

// The values each partial in web/templates/partials takes. A page builds
// these in Go; templates never assemble a control from loose strings.

type Button struct {
	Kind          string // primary, secondary, danger, danger-outline, ghost
	Label         string
	Type          string // submit (default) or button
	ID            string
	Name, Value   string
	Disabled      bool
	Busy          bool
	Wide          bool
	Command       string
	CommandFor    string
	PopoverTarget string
	Pressed       string
}

type LinkButton struct {
	Kind, Label, Href string
	Wide              bool
}

type Field struct {
	ID, Name, Label, Hint string
	Type                  string // text (default), email, password, number, tel
	Value                 string
	Error                 string
	Autocomplete          string
	Inputmode             string
	List                  string   // the datalist id, when Options is set
	Options               []string // suggestions
	MaxLength             int
	Required              bool
	Disabled              bool
	ReadOnly              bool
	Autofocus             bool
}

// Select is a drop-down of options. With Trigger set it saves when changed
// (DEC-98), posting to Trigger by htmx.
type Select struct {
	ID, Name, Label, Hint string
	Options               []Option
	Error                 string
	Trigger               string // the URL a change posts to, or ""
}

type Option struct {
	Value, Label string
	Selected     bool
}

type Choice struct {
	ID, Name, Type, Value string // Type is checkbox or radio
	Label, Hint           string
	Checked, Disabled     bool
	Error                 string
}

type Chip struct {
	Label, Name, Value, Type string
	Pressed, Disabled        bool
}

type Segment struct {
	Label, Name string
	Options     []SegmentOption
}

type SegmentOption struct {
	Label, Value      string
	Pressed, Disabled bool
}

type Menu struct {
	ID, Label string
	Items     []MenuItem
}

type MenuItem struct{ Label, Href string }

type Dialog struct {
	ID                  string
	OpenLabel, OpenKind string
	Title, Text         string
	CancelLabel         string
	ConfirmLabel        string
	ConfirmKind         string
	Action, CSRF        string
}

type Empty struct {
	Image, Title, Text string
	Action             *LinkButton
	Main               bool
}

type Notice struct {
	Tone        string // info, success, warning, danger
	Title, Text string
	ID          string
	Links       []LinkButton
}

type FieldError struct{ ID, Message string }

// Hidden is a value a form carries back unchanged.
type Hidden struct{ Name, Value string }

// NoticeItem is one row of the notice list.
type NoticeItem struct {
	ID               int64
	Text, Note, When string
	Read             bool
	CSRF             string
}

// errorSummary lists the fields' errors in form order.
func errorSummary(fields ...Field) []FieldError {
	var out []FieldError
	for _, f := range fields {
		if f.Error != "" {
			out = append(out, FieldError{ID: f.ID, Message: f.Error})
		}
	}
	return out
}

// focusFirstError sets autofocus on the first field with an error.
func focusFirstError(fields ...*Field) {
	for _, f := range fields {
		if f.Error != "" {
			f.Autofocus = true
			return
		}
	}
}
