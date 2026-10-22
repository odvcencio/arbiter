package expert

import (
	"fmt"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/expert/engine"
)

// Compile parses .arb source and extracts only expert rules into an expert program.
func Compile(source []byte) (*Program, error) {
	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return nil, err
	}
	base, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, err
	}
	return CompileParsed(parsed, base)
}

// CompileParsed extracts expert rules from a previously parsed source.
func CompileParsed(parsed *arbiter.ParsedSource, base *arbiter.CompileResult) (*Program, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed source")
	}
	if base == nil {
		var err error
		base, err = arbiter.CompileFullParsed(parsed)
		if err != nil {
			return nil, err
		}
	}
	if base.Program == nil {
		return nil, fmt.Errorf("nil lowered program")
	}
	return engine.NewProgram(base.Program, base.Segments)
}

// CompileFile resolves includes and compiles expert rules from a root .arb file.
func CompileFile(path string) (*Program, error) {
	unit, parsed, err := arbiter.LoadFileParsed(path)
	if err != nil {
		return nil, err
	}
	base, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	program, err := CompileParsed(parsed, base)
	if err != nil {
		return nil, arbiter.WrapFileError(unit, err)
	}
	return program, nil
}
