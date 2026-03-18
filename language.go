package arbiter

import gotreesitter "github.com/odvcencio/gotreesitter"

// GetLanguage returns the compiled arbiter tree-sitter language.
// It is safe for concurrent use (internally cached).
func GetLanguage() (*gotreesitter.Language, error) {
	return getArbiterLanguage()
}
