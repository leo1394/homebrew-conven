package materialize

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func requireExistingGuard(driver Driver, path string, key string) error {
	if driver == DriverYAMLOverlay {
		return requireExistingYAMLGuard(path, key)
	}
	_, found, err := readProperty(path, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("property %q does not exist", key)
	}
	return nil
}

func applyConfigPatch(driver Driver, path string, key string, value any) error {
	if driver == DriverYAMLOverlay {
		return applyYAMLPatch(path, key, value)
	}
	return writeProperty(path, key, propertyValue(value), true)
}

func applyConfigGuard(driver Driver, path string, key string, value any, allowCreate bool) error {
	if driver == DriverYAMLOverlay {
		return applyYAMLGuard(path, key, value, allowCreate)
	}
	return writeProperty(path, key, propertyValue(value), allowCreate)
}

func verifyConfigGuard(driver Driver, path string, key string, value any) error {
	if driver == DriverYAMLOverlay {
		return verifyYAMLGuard(path, key, value)
	}
	actual, found, err := readProperty(path, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("property %q does not exist", key)
	}
	expected := escapePropertyValue(propertyValue(value))
	if actual != expected && actual != propertyValue(value) {
		return fmt.Errorf("property %q is %q, want %q", key, actual, propertyValue(value))
	}
	return nil
}

func readProperty(path string, key string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(string(data), "\n")
	value := ""
	found := false
	for index, line := range lines {
		candidate, parsed, err := parsePropertyLine(line)
		if err != nil {
			return "", false, fmt.Errorf("line %d: %w", index+1, err)
		}
		if !parsed || candidate.key != key {
			continue
		}
		if found {
			return "", false, fmt.Errorf("property %q is duplicated", key)
		}
		found = true
		value = candidate.value
	}
	return value, found, nil
}

func writeProperty(path string, key string, value string, allowCreate bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	found := -1
	for index, line := range lines {
		candidate, parsed, err := parsePropertyLine(line)
		if err != nil {
			return fmt.Errorf("line %d: %w", index+1, err)
		}
		if !parsed || candidate.key != key {
			continue
		}
		if found >= 0 {
			return fmt.Errorf("property %q is duplicated", key)
		}
		found = index
	}
	if found < 0 {
		if !allowCreate {
			return fmt.Errorf("property %q does not exist", key)
		}
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, key+"="+escapePropertyValue(value), "")
	} else {
		lines[found] = key+"="+escapePropertyValue(value)
	}
	return writePrivateFile(path, []byte(strings.Join(lines, "\n")))
}

type propertyEntry struct {
	key   string
	value string
}

func parsePropertyLine(line string) (propertyEntry, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
		return propertyEntry{}, false, nil
	}
	separator := -1
	escaped := false
	for index, character := range line {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if character == '=' || character == ':' {
			separator = index
			break
		}
	}
	if separator < 1 {
		return propertyEntry{}, false, fmt.Errorf("property must use key=value or key:value syntax")
	}
	key := strings.TrimSpace(line[:separator])
	if key == "" {
		return propertyEntry{}, false, fmt.Errorf("property key is empty")
	}
	return propertyEntry{key: key, value: strings.TrimSpace(line[separator+1:])}, true, nil
}

func propertyValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func escapePropertyValue(value string) string {
	var result strings.Builder
	writer := bufio.NewWriter(&result)
	for _, character := range value {
		switch character {
		case '\\', ':', '=':
			writer.WriteByte('\\')
			writer.WriteRune(character)
		case '\n':
			writer.WriteString("\\n")
		case '\r':
			writer.WriteString("\\r")
		default:
			writer.WriteRune(character)
		}
	}
	writer.Flush()
	return result.String()
}
