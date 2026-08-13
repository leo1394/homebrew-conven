package cli

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/leo1394/homebrew-conven/examples"
)

func workspacePolicySpecification() []byte {
	if chineseLocale(preferredSystemLocale()) {
		return examples.WorkspacePolicyGeneratorAISpec
	}
	return examples.WorkspacePolicyGeneratorAISpecEnglish
}

func preferredSystemLocale() string {
	return preferredLocale(runtime.GOOS, os.LookupEnv, readAppleLanguages)
}

func preferredLocale(goos string, lookupEnv func(string) (string, bool), appleLanguages func() string) string {
	locale, _ := environmentLocale(lookupEnv)
	if goos == "darwin" {
		if appleLocale := firstAppleLanguage(appleLanguages()); appleLocale != "" {
			return appleLocale
		}
	}
	return locale
}

func environmentLocale(lookupEnv func(string) (string, bool)) (string, bool) {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		value, exists := lookupEnv(name)
		if !exists || strings.TrimSpace(value) == "" {
			continue
		}
		if name == "LANGUAGE" {
			for _, candidate := range strings.Split(value, ":") {
				if strings.TrimSpace(candidate) != "" {
					return candidate, true
				}
			}
			continue
		}
		return value, true
	}
	return "", false
}

func chineseLocale(locale string) bool {
	locale = normalizedLocale(locale)
	return locale == "zh" || strings.HasPrefix(locale, "zh-") || strings.HasPrefix(locale, "zh_")
}

func normalizedLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if index := strings.IndexByte(locale, '.'); index >= 0 {
		locale = locale[:index]
	}
	if index := strings.IndexByte(locale, '@'); index >= 0 {
		locale = locale[:index]
	}
	return locale
}

func readAppleLanguages() string {
	output, err := exec.Command("/usr/bin/defaults", "read", "NSGlobalDomain", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	return string(output)
}

func firstAppleLanguage(value string) string {
	if start := strings.IndexByte(value, '"'); start >= 0 {
		if end := strings.IndexByte(value[start+1:], '"'); end >= 0 {
			return strings.TrimSpace(value[start+1 : start+1+end])
		}
	}
	for _, field := range strings.Fields(value) {
		field = strings.Trim(field, "(),\"' \t\r\n")
		if field != "" {
			return field
		}
	}
	return ""
}
