package fangort

import (
	"encoding/hex"
	"strconv"
	"strings"
)

// linkOps spells the operator characters Go cannot hold in an identifier.
// Words rather than a hash because generated Go is meant to be readable:
// `Basics.++` becomes `v_Basics_dot__plus__plus_`, which can be traced back
// to its source by eye — and by Demangle, which stack traces rely on.
var linkOps = map[byte]string{
	'!': "_bang_", '#': "_hash_", '%': "_pct_", '&': "_amp_", '*': "_star_",
	'+': "_plus_", '-': "_dash_", '/': "_slash_", ':': "_colon_", '<': "_lt_",
	'=': "_eq_", '>': "_gt_", '?': "_qmark_", '@': "_at_", '^': "_hat_",
	'|': "_bar_", '~': "_tilde_",
}

// LinkName maps a canonical Fango symbol to a Go identifier. Module
// separators and operator characters are the only characters a Fango symbol
// can hold that Go cannot.
//
// The substitution is injective against ordinary names: identifier
// characters and operator characters are disjoint sets, a Fango name may
// not begin with `_`, and the character after a `_dot_` is always a letter —
// so no identifier can spell one of these words in the position where an
// operator's would appear. It is a pure function of the name, which is what
// the byte-identical-Go requirement needs.
func LinkName(name string) string {
	if !strings.ContainsFunc(name, func(r rune) bool {
		_, isOp := linkOps[byte(r)]
		return r == '.' || (r < 0x80 && isOp)
	}) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '.':
			b.WriteString("_dot_")
		default:
			if word, isOp := linkOps[c]; isOp {
				b.WriteString(word)
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Symbol is what a generated Go function name says about its Fango origin.
type Symbol struct {
	// Module is the Fango module owning the function; empty for a headerless
	// entry program.
	Module string
	// Function is the source-level name within the module: `divide`,
	// `outer.inner` for a local function, `Show (List a).show` for an
	// instance method, or `<lambda>` for a shared callback body.
	Function string
	// Lambda reports a function literal nested in Function.
	Lambda bool
}

// String renders the symbol the way a trace names it.
func (s Symbol) String() string {
	name := s.Function
	if s.Module != "" {
		name = s.Module + "." + name
	}
	if s.Lambda {
		name += " (lambda)"
	}
	return name
}

const modulePackagePrefix = "fangobuild/modules/"

// Demangle recovers the Fango symbol behind a Go function name as the
// runtime reports it, such as `fangobuild/modules/Main.V_Main_dot_divide` or
// `main.V_main.func1`. Functions outside generated Fango packages, and the
// package-level plumbing inside them, are not symbols.
func Demangle(goFunction string) (Symbol, bool) {
	name := stripInstantiation(goFunction)
	pkgPath, local := name, ""
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		pkgPath, local = name[:i+1], name[i+1:]
	} else {
		pkgPath, local = "", name
	}
	pkgName, fn, ok := strings.Cut(local, ".")
	if !ok {
		return Symbol{}, false
	}
	pkgPath += pkgName
	var module string
	switch {
	case pkgPath == "main":
	case strings.HasPrefix(pkgPath, modulePackagePrefix):
		module = strings.ReplaceAll(strings.TrimPrefix(pkgPath, modulePackagePrefix), "/", ".")
	default:
		return Symbol{}, false
	}
	base, rest, _ := strings.Cut(fn, ".")
	lambda := rest != ""
	// A shared callback body and a function literal inside a package-level
	// value's initializer have no enclosing Fango function.
	if strings.HasPrefix(base, "callbackBody") || (base == "init" && lambda) {
		return Symbol{Module: module, Function: "<lambda>"}, true
	}
	link, ok := strings.CutPrefix(base, "V_")
	if !ok {
		return Symbol{}, false
	}
	link = strings.TrimSuffix(link, "_exit")
	symbol := unlinkSymbol(link)
	owner, function := splitOwner(symbol)
	if owner == "" {
		owner = module
	}
	return Symbol{Module: owner, Function: function, Lambda: lambda}, true
}

// SymbolName spells a canonical Fango definition name the way a trace does:
// lifted locals and instance methods regain their source form and anything
// else is unchanged. The interpreter names its frames with it, so both
// backends print one spelling.
func SymbolName(canonical string) string {
	return unlinkSymbol(LinkName(canonical))
}

// stripInstantiation removes a generic instantiation suffix, which may nest
// brackets and contain package paths of its own.
func stripInstantiation(name string) string {
	open := strings.IndexByte(name, '[')
	if open < 0 {
		return name
	}
	depth := 0
	for i := open; i < len(name); i++ {
		switch name[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return name[:open] + name[i+1:]
			}
		}
	}
	return name[:open]
}

// unlinkSymbol inverts LinkName on a top-level symbol, then rewrites the
// compiler's synthetic names (lifted locals and instance methods) into their
// source spelling.
func unlinkSymbol(link string) string {
	if lifted, ok := strings.CutPrefix(link, "_lift"); ok {
		return unlinkLifted(lifted)
	}
	canonical := unlink(link)
	owner, local := splitOwner(canonical)
	if decoded, ok := unlinkInstance(local); ok {
		local = decoded
	}
	if owner != "" {
		return owner + "." + local
	}
	return local
}

// unlink inverts LinkName. A segment that consists only of operator words is
// an operator; any other `_word_` is an ordinary identifier's own text.
func unlink(link string) string {
	segments := strings.Split(link, "_dot_")
	for i, segment := range segments {
		if op, ok := unlinkOperator(segment); ok {
			segments[i] = op
		}
	}
	return strings.Join(segments, ".")
}

func unlinkOperator(segment string) (string, bool) {
	if segment == "" {
		return "", false
	}
	var b strings.Builder
	for rest := segment; rest != ""; {
		c, ok := operatorWord(rest)
		if !ok {
			return "", false
		}
		b.WriteByte(c)
		rest = rest[len(linkOps[c]):]
	}
	return b.String(), true
}

// operatorWord finds the operator word at the start of s. No word is a
// prefix of another, so the first match is the only one.
func operatorWord(s string) (byte, bool) {
	for c, word := range linkOps {
		if strings.HasPrefix(s, word) {
			return c, true
		}
	}
	return 0, false
}

// splitOwner separates a canonical symbol into its module and local name.
// An operator's local name starts with an operator character, so the last
// dot before an identifier character is the separator.
func splitOwner(symbol string) (owner, local string) {
	i := strings.LastIndexByte(symbol, '.')
	if i < 0 {
		return "", symbol
	}
	return symbol[:i], symbol[i+1:]
}

// unlinkLifted decodes `_lift_<decl>_<seq>_<name>` (stable lifts) and
// `_lift<n>_<name>` (generation lifts) into `decl.name` or `name`.
func unlinkLifted(rest string) string {
	if after, ok := strings.CutPrefix(rest, "_"); ok {
		// Stable form: the declaring definition's link name, a sequence
		// number, then the local name. The sequence is the last all-digit
		// segment between underscores.
		for i := len(after) - 1; i > 0; i-- {
			if after[i] != '_' {
				continue
			}
			j := i - 1
			for j >= 0 && after[j] >= '0' && after[j] <= '9' {
				j--
			}
			if j < i-1 && j >= 0 && after[j] == '_' {
				return unlink(after[:j]) + "." + after[i+1:]
			}
		}
		return after
	}
	if _, name, ok := strings.Cut(rest, "_"); ok {
		return name
	}
	return rest
}

// unlinkInstance decodes `_instance_<hex class>_<hex head>[_context_<hex>]
// [_generation_<n>...]_<method>` into `Class (Head).method`.
func unlinkInstance(local string) (string, bool) {
	rest, ok := strings.CutPrefix(local, "_instance_")
	if !ok {
		return "", false
	}
	classHex, rest, ok := strings.Cut(rest, "_")
	if !ok {
		return "", false
	}
	headHex, rest, ok := strings.Cut(rest, "_")
	if !ok {
		return "", false
	}
	class, err := hex.DecodeString(classHex)
	if err != nil {
		return "", false
	}
	head, err := hex.DecodeString(headHex)
	if err != nil {
		return "", false
	}
	if after, ok := strings.CutPrefix(rest, "context_"); ok {
		if _, after, ok = strings.Cut(after, "_"); !ok {
			return "", false
		}
		rest = after
	}
	if after, ok := strings.CutPrefix(rest, "generation_"); ok {
		// Unique numbers, one per underscore-separated segment, until the
		// method name, which starts with a letter.
		rest = after
		for {
			segment, more, found := strings.Cut(rest, "_")
			if _, err := strconv.Atoi(segment); err != nil {
				break
			}
			if !found {
				return "", false
			}
			rest = more
		}
	}
	if rest == "" {
		return "", false
	}
	_, className := splitOwner(string(class))
	return className + " (" + surfaceHead(string(head)) + ")." + rest, true
}

// surfaceHead rewrites a canonical instance head key such as
// `Main.List(Main.Item,$0)` into `List Item a`: type names lose their module
// qualifier and numbered variables become letters.
func surfaceHead(key string) string {
	var b, word strings.Builder
	flush := func() {
		b.WriteString(word.String())
		word.Reset()
	}
	for i := 0; i < len(key); i++ {
		switch c := key[i]; c {
		case '$':
			j := i + 1
			for j < len(key) && key[j] >= '0' && key[j] <= '9' {
				j++
			}
			index, _ := strconv.Atoi(key[i+1 : j])
			word.WriteByte(byte('a' + index%26))
			i = j - 1
		case '.':
			word.Reset()
		case '(', ',':
			flush()
			b.WriteByte(' ')
		case ')':
			flush()
		default:
			word.WriteByte(c)
		}
	}
	flush()
	return strings.TrimSpace(b.String())
}
