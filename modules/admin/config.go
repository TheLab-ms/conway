package admin

type configSection struct {
	Name string
	Path string
}

func secretPlaceholder(hasValue bool) string {
	if hasValue {
		return "(secret is set - leave blank to keep)"
	}
	return "(not set)"
}

func secretHelpText(hasValue bool) string {
	if hasValue {
		return "Leave blank to keep the current value."
	}
	return ""
}
