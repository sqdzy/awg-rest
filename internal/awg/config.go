package awg

import "strings"

// InterfaceValue returns a key from the [Interface] section of an awg config.
// Matching is case-insensitive and ignores comments/blank lines.
func InterfaceValue(config, key string) string {
	var section string
	want := strings.ToLower(strings.TrimSpace(key))
	for _, raw := range strings.Split(config, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(strings.Trim(line, "[]")))
			continue
		}
		if section != "interface" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(k)) == want {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// WithInterfaceValue inserts key/value into the [Interface] section when the
// key is absent. Existing values are preserved.
func WithInterfaceValue(config, key, value string) string {
	if strings.TrimSpace(value) == "" || InterfaceValue(config, key) != "" {
		return config
	}
	lines := strings.SplitAfter(config, "\n")
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "[Interface]") {
			insert := key + " = " + value + "\n"
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, insert)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "")
		}
	}
	return "[Interface]\n" + key + " = " + value + "\n" + config
}

// PreserveInterfacePrivateKey copies PrivateKey from currentConfig into config
// when config does not already include it. awg syncconf treats a missing
// PrivateKey as an empty interface key, so the reconciler must preserve it.
func PreserveInterfacePrivateKey(config, currentConfig string) string {
	return WithInterfaceValue(config, "PrivateKey", InterfaceValue(currentConfig, "PrivateKey"))
}

// sanitizeSyncConf removes empty optional AmneziaWG V2 special-junk keys before
// feeding stripped config into `awg syncconf`. amneziawg-tools accepts blank
// I1-I5 in client-style files, but `awg syncconf` rejects lines such as `I2=`.
func sanitizeSyncConf(config string) string {
	var b strings.Builder
	for _, raw := range strings.SplitAfter(config, "\n") {
		if isEmptySpecialJunkLine(raw) {
			continue
		}
		b.WriteString(raw)
	}
	return b.String()
}

func isEmptySpecialJunkLine(raw string) bool {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return false
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "i1", "i2", "i3", "i4", "i5":
		return strings.TrimSpace(value) == ""
	default:
		return false
	}
}
