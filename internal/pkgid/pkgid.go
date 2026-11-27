// Package pkgid defines package identities shared by recipes, the installed
// database, dependency atoms and binary package caches.
//
// A package is installed at most once per (name, slot). Slot "0" is the
// default and keeps the legacy plain-name spelling, so identities written by
// older zpkg versions need no migration; any other slot is rendered as
// "name@slot".
package pkgid

import "strings"

// Key is the identity of one package slot: "name" for slot 0 (or an empty
// slot), "name@slot" otherwise.
func Key(name, slot string) string {
	if slot == "" || slot == "0" {
		return name
	}
	return name + "@" + slot
}

// Split parses a package key back into name and slot. A key without "@"
// belongs to slot "0".
func Split(key string) (name, slot string) {
	if i := strings.LastIndexByte(key, '@'); i > 0 {
		return key[:i], key[i+1:]
	}
	return key, "0"
}

// ValidSlot reports whether slot is safe to use as part of a package key and
// therefore as a directory name in the installed database.
func ValidSlot(slot string) bool {
	if slot == "" || slot == "." || slot == ".." {
		return false
	}
	for _, r := range slot {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '+', r == '-':
		default:
			return false
		}
	}
	return true
}
