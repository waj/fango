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
	LIDENT // lower-case identifier
	UIDENT // upper-case identifier (constructors, types, module names)

	// Punctuation and operators.
	EQ     // =
	PLUS   // +
	MINUS  // -
	STAR   // *
	SLASH  // /
	LPAREN // (
	RPAREN // )
	COMMA  // ,
	DOT    // .
	DOTDOT // ..

	// Remaining operators.
	PLUSPLUS // ++
	EQEQ     // ==
	SLASHEQ  // /=
	LT       // <
	GT       // >
	LTEQ     // <=
	GTEQ     // >=
	ANDAND   // &&
	OROR     // ||
	ARROW    // ->
	DARROW   // =>
	BACKSLASH
	COLON      // :
	PIPE       // |
	LBRACE     // {
	RBRACE     // }
	UNDERSCORE // _ (wildcard pattern)

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
	KwClass
	KwInstance
	KwDeriving
)

var kindNames = map[Kind]string{
	EOF: "EOF", INT: "INT", FLOAT: "FLOAT", STRING: "STRING",
	LIDENT: "LIDENT", UIDENT: "UIDENT",
	EQ: "EQ", PLUS: "PLUS", MINUS: "MINUS", STAR: "STAR", SLASH: "SLASH",
	LPAREN: "LPAREN", RPAREN: "RPAREN", COMMA: "COMMA", DOT: "DOT", DOTDOT: "DOTDOT",
	PLUSPLUS: "PLUSPLUS", EQEQ: "EQEQ", SLASHEQ: "SLASHEQ",
	LT: "LT", GT: "GT", LTEQ: "LTEQ", GTEQ: "GTEQ",
	ANDAND: "ANDAND", OROR: "OROR",
	ARROW: "ARROW", BACKSLASH: "BACKSLASH", COLON: "COLON", PIPE: "PIPE",
	LBRACE: "LBRACE", RBRACE: "RBRACE", UNDERSCORE: "UNDERSCORE",
	KwModule: "module", KwImport: "import", KwAs: "as", KwExposing: "exposing", KwLet: "let", KwIn: "in",
	KwIf: "if", KwThen: "then", KwElse: "else", KwCase: "case", KwOf: "of",
	KwType: "type", KwEffect: "effect", KwHandle: "handle", KwResume: "resume",
	KwNative: "native", KwInfix: "infix",
	KwClass: "class", KwInstance: "instance", KwDeriving: "deriving", DARROW: "DARROW",
}

func (k Kind) String() string { return kindNames[k] }

// Keywords maps source text to reserved-keyword kinds.
var Keywords = map[string]Kind{
	"module": KwModule, "import": KwImport, "as": KwAs, "exposing": KwExposing,
	"let": KwLet, "in": KwIn,
	"if": KwIf, "then": KwThen, "else": KwElse,
	"case": KwCase, "of": KwOf,
	"type": KwType, "effect": KwEffect, "handle": KwHandle, "resume": KwResume,
	"native": KwNative, "infix": KwInfix,
	"class": KwClass, "instance": KwInstance, "deriving": KwDeriving,
}

type Token struct {
	Kind Kind
	Text string
	Span source.Span
}

// Pos returns the token's 1-based start position (the layout rules are
// stated in terms of token columns).
func (t Token) Pos() source.Pos { return t.Span.StartPos() }
