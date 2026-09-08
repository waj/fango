// Package token defines the token kinds produced by the lexer. All keywords
// of the language are reserved so their spelling cannot be used as names.
package token

import "github.com/waj/fango/internal/source"

type Kind int

const (
	EOF Kind = iota
	INT
	FLOAT
	STRING
	CHAR
	LIDENT // lower-case identifier
	UIDENT // upper-case identifier (constructors, types, module names)

	// OP is a run of operator characters that is not one of the reserved
	// lexemes below. Its Text is the spelling, which is also the operator's
	// name: `(+)` declares the value the lexer reports as OP "+". Operator
	// characters and identifier characters are disjoint sets, so an operator
	// name can never be confused with an ordinary one.
	OP

	// Punctuation. None of these characters is an operator character, so
	// none of them ever takes part in an operator run.
	LPAREN // (
	RPAREN // )
	COMMA  // ,
	DOT    // .
	DOTDOT // ..
	LBRACE // {
	RBRACE // }
	BACKSLASH
	UNDERSCORE  // _ (wildcard pattern)
	DOLLARPAREN // $( — opens a splice; `$` is never a token on its own

	// Reserved operator lexemes: operator runs the grammar recognizes by
	// kind rather than by spelling. A run becomes one of these only when it
	// equals the lexeme exactly, so `->` is ARROW while `->>` is OP.
	EQ     // =
	ARROW  // ->
	DARROW // =>
	COLON  // :
	PIPE   // |
	CARET  // ^ (pinned value pattern)

	// Reserved keywords.
	KwModule
	KwImport
	KwAs
	KwExposing
	KwLet
	KwIn
	KwIf
	KwThen
	KwElse
	KwCase
	KwOf
	KwType
	KwEffect
	KwHandle
	KwResume
	KwNative
	KwInfix
	KwInfixL
	KwInfixR
	KwClass
	KwInstance
	KwDeriver
	KwDeriving
	KwQuote
	KwTypeOf
)

var kindNames = map[Kind]string{
	EOF: "EOF", INT: "INT", FLOAT: "FLOAT", STRING: "STRING", CHAR: "CHAR",
	LIDENT: "LIDENT", UIDENT: "UIDENT",
	OP:     "OP",
	LPAREN: "LPAREN", RPAREN: "RPAREN", COMMA: "COMMA", DOT: "DOT", DOTDOT: "DOTDOT",
	LBRACE: "LBRACE", RBRACE: "RBRACE", BACKSLASH: "BACKSLASH",
	UNDERSCORE: "UNDERSCORE", DOLLARPAREN: "DOLLARPAREN",
	EQ: "EQ", ARROW: "ARROW", DARROW: "DARROW", COLON: "COLON", PIPE: "PIPE", CARET: "CARET",
	KwModule: "module", KwImport: "import", KwAs: "as", KwExposing: "exposing", KwLet: "let", KwIn: "in",
	KwIf: "if", KwThen: "then", KwElse: "else", KwCase: "case", KwOf: "of",
	KwType: "type", KwEffect: "effect", KwHandle: "handle", KwResume: "resume",
	KwNative: "native", KwInfix: "infix", KwInfixL: "infixl", KwInfixR: "infixr",
	KwClass: "class", KwInstance: "instance", KwDeriver: "deriver", KwDeriving: "deriving",
	KwQuote: "quote", KwTypeOf: "typeOf",
}

func (k Kind) String() string { return kindNames[k] }

// Keywords maps source text to reserved-keyword kinds.
var Keywords = map[string]Kind{
	"module": KwModule, "import": KwImport, "as": KwAs, "exposing": KwExposing,
	"let": KwLet, "in": KwIn,
	"if": KwIf, "then": KwThen, "else": KwElse,
	"case": KwCase, "of": KwOf,
	"type": KwType, "effect": KwEffect, "handle": KwHandle, "resume": KwResume,
	"native": KwNative, "infix": KwInfix, "infixl": KwInfixL, "infixr": KwInfixR,
	"class": KwClass, "instance": KwInstance, "deriver": KwDeriver, "deriving": KwDeriving,
	"quote": KwQuote, "typeOf": KwTypeOf,
}

// opChars is the operator character class. An operator name is a non-empty
// run of these, so the set is the language's whole operator vocabulary.
//
// Four plausible characters are deliberately absent. `.` is record access
// (`r.field`), module qualification (`List.foldl`), and `..`; because a dot
// in a name means "module separator" everywhere in name resolution, an
// operator containing one could not be told from a qualified reference. `$`
// would make `f $(x)` a splice but `f $ (x)` an application. `\` keeps
// lambda unambiguous, and `,` `(` `)` `{` `}` are punctuation.
//
// A run may not begin with `--`, which is a line comment and is consumed
// before operator scanning.
const opChars = "!#%&*+-/:<=>?@^|~"

// IsOpChar reports whether c may appear in an operator name.
func IsOpChar(c byte) bool {
	for i := 0; i < len(opChars); i++ {
		if opChars[i] == c {
			return true
		}
	}
	return false
}

// reservedOps are the operator runs the grammar matches by kind. A run
// becomes OP unless it equals one of these exactly.
var reservedOps = map[string]Kind{
	"=": EQ, "->": ARROW, "=>": DARROW, ":": COLON, "|": PIPE, "^": CARET,
}

// OpKind returns the kind for a complete operator run: its reserved kind if
// the spelling is reserved, otherwise OP.
func OpKind(text string) Kind {
	if k, ok := reservedOps[text]; ok {
		return k
	}
	return OP
}

// IsReservedOp reports whether an operator spelling is reserved by the
// grammar and therefore unavailable as a name.
func IsReservedOp(text string) bool {
	_, ok := reservedOps[text]
	return ok
}

type Token struct {
	Kind Kind
	Text string
	Span source.Span
}

// IsOp reports whether the token is a usable binary or prefix operator —
// an OP token. Reserved lexemes have their own kinds and are excluded.
func (t Token) IsOp() bool { return t.Kind == OP }

// Pos returns the token's 1-based start position (the layout rules are
// stated in terms of token columns).
func (t Token) Pos() source.Pos { return t.Span.StartPos() }
