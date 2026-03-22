package arbiter

import (
	"sync"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

var (
	arbLangOnce   sync.Once
	arbLangCached *gotreesitter.Language
	arbLangErr    error
)

func getArbiterLanguage() (*gotreesitter.Language, error) {
	arbLangOnce.Do(func() {
		arbLangCached, arbLangErr = GenerateLanguage(ArbiterGrammar())
	})
	return arbLangCached, arbLangErr
}

// GetLanguage returns the compiled arbiter tree-sitter language.
// It is safe for concurrent use (internally cached).
func GetLanguage() (*gotreesitter.Language, error) {
	return getArbiterLanguage()
}
