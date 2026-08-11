package jdk

import (
	"strconv"
	"strings"
	"unicode"
)

// Remote describes a JDK archive available from the configured provider.
type Remote struct {
	Distribution string
	Feature      int
	Version      string
	FileName     string
	URL          string
	Checksum     string
	Size         int64
}

func (remote Remote) ID() string {
	if remote.Distribution == "" {
		return remote.Version
	}
	return remote.Distribution + "@" + remote.Version
}

// Installed describes a JDK in the local managed store.
type Installed struct {
	Distribution string
	Version      string
	Path         string
	Active       bool
}

func (installed Installed) ID() string {
	if installed.Distribution == "" {
		return installed.Version
	}
	return installed.Distribution + "@" + installed.Version
}

func Feature(version string) int {
	end := 0
	for end < len(version) && version[end] >= '0' && version[end] <= '9' {
		end++
	}
	feature, _ := strconv.Atoi(version[:end])
	return feature
}

func CompareVersions(left, right string) int {
	leftParts := numericParts(left)
	rightParts := numericParts(right)
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		if leftParts[index] != rightParts[index] {
			return leftParts[index] - rightParts[index]
		}
	}
	if len(leftParts) != len(rightParts) {
		return len(leftParts) - len(rightParts)
	}
	return strings.Compare(left, right)
}

func numericParts(version string) []int {
	parts := strings.FieldsFunc(version, func(character rune) bool { return !unicode.IsDigit(character) })
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		number, _ := strconv.Atoi(part)
		numbers = append(numbers, number)
	}
	return numbers
}
