// Package token defines the token kinds produced by the lexer. All keywords
// of the full language are reserved from S0 so programs never break when
// features land.
package token

import "github.com/waj/fango/internal/source"

type Kind int

const (
	EOF Kind = iota
	INT
	FLOAT  // reserved: lexed from S1
	STRING // reserved: lexed from S1
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

	// Reserved operators (S1+).
	PLUSPLUS // ++
	EQEQ     // ==
	SLASHEQ  // /=
	LT       // <
	GT       // >
	LTEQ     // <=
	GTEQ     // >=
	ARROW    // ->
	BACKSLASH
	COLON      // :
	PIPE       // |
	LBRACE     // {
	RBRACE     // }
	UNDERSCORE // _ (wildcard pattern)

	// Reserved keywords.
	KwModule
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
)

var kindNames = map[Kind]string{
	EOF: "EOF", INT: "INT", FLOAT: "FLOAT", STRING: "STRING",
	LIDENT: "LIDENT", UIDENT: "UIDENT",
	EQ: "EQ", PLUS: "PLUS", MINUS: "MINUS", STAR: "STAR", SLASH: "SLASH",
	LPAREN: "LPAREN", RPAREN: "RPAREN", COMMA: "COMMA",
	PLUSPLUS: "PLUSPLUS", EQEQ: "EQEQ", SLASHEQ: "SLASHEQ",
	LT: "LT", GT: "GT", LTEQ: "LTEQ", GTEQ: "GTEQ",
	ARROW: "ARROW", BACKSLASH: "BACKSLASH", COLON: "COLON", PIPE: "PIPE",
	LBRACE: "LBRACE", RBRACE: "RBRACE", UNDERSCORE: "UNDERSCORE",
	KwModule: "module", KwExposing: "exposing", KwLet: "let", KwIn: "in",
	KwIf: "if", KwThen: "then", KwElse: "else", KwCase: "case", KwOf: "of",
	KwType: "type", KwEffect: "effect", KwHandle: "handle", KwResume: "resume",
}

func (k Kind) String() string { return kindNames[k] }

// Keywords maps source text to reserved-keyword kinds.
var Keywords = map[string]Kind{
	"module": KwModule, "exposing": KwExposing,
	"let": KwLet, "in": KwIn,
	"if": KwIf, "then": KwThen, "else": KwElse,
	"case": KwCase, "of": KwOf,
	"type": KwType, "effect": KwEffect, "handle": KwHandle, "resume": KwResume,
}

type Token struct {
	Kind Kind
	Text string
	Span source.Span
}

// Pos returns the token's 1-based start position (the layout rules are
// stated in terms of token columns).
func (t Token) Pos() source.Pos { return t.Span.StartPos() }
