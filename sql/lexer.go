package sql

import (
	"fmt"
	"strings"
)

// TokenKind classifies a lexical token.
type TokenKind int

const (
	TokEOF TokenKind = iota
	TokIdent
	TokInt
	TokFloat
	TokString
	TokOp
	TokComma
	TokSemicolon
	TokLParen
	TokRParen
	TokStar
	TokDot
)

// Token is a single lexical token.
type Token struct {
	Kind TokenKind
	Val  string
	Pos  int
}

var keywords = map[string]bool{
	"select": true, "from": true, "where": true, "join": true, "inner": true,
	"left": true, "right": true, "outer": true, "on": true, "as": true,
	"and": true, "or": true, "not": true, "null": true, "group": true,
	"by": true, "having": true, "order": true, "limit": true, "offset": true,
	"asc": true, "desc": true, "distinct": true, "in": true, "between": true,
	"is": true, "like": true, "count": true, "sum": true, "avg": true,
	"min": true, "max": true, "cross": true, "full": true, "case": true,
	"when": true, "then": true, "else": true, "end": true, "union": true,
	"all": true, "exists": true,
}

// IsKeyword reports whether s is a reserved keyword.
func IsKeyword(s string) bool { return keywords[strings.ToLower(s)] }

type lexer struct {
	input string
	pos   int
}

// Tokenize turns a SQL string into tokens.
func Tokenize(input string) ([]Token, error) {
	l := &lexer{input: input}
	var toks []Token
	for {
		l.skipSpace()
		if l.pos >= len(l.input) {
			toks = append(toks, Token{Kind: TokEOF, Pos: l.pos})
			return toks, nil
		}
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, tok)
	}
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			l.pos++
			continue
		}
		if c == '-' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '-' {
			for l.pos < len(l.input) && l.input[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		break
	}
}

func (l *lexer) next() (Token, error) {
	start := l.pos
	c := l.input[l.pos]
	switch {
	case isIdentStart(c):
		return l.ident(start)
	case c >= '0' && c <= '9':
		return l.number(start)
	case c == '\'':
		return l.string(start)
	}
	switch c {
	case ',':
		l.pos++
		return Token{Kind: TokComma, Val: ",", Pos: start}, nil
	case ';':
		l.pos++
		return Token{Kind: TokSemicolon, Val: ";", Pos: start}, nil
	case '(':
		l.pos++
		return Token{Kind: TokLParen, Val: "(", Pos: start}, nil
	case ')':
		l.pos++
		return Token{Kind: TokRParen, Val: ")", Pos: start}, nil
	case '*':
		l.pos++
		return Token{Kind: TokStar, Val: "*", Pos: start}, nil
	case '.':
		l.pos++
		return Token{Kind: TokDot, Val: ".", Pos: start}, nil
	case '+', '-', '/', '=':
		l.pos++
		return Token{Kind: TokOp, Val: string(c), Pos: start}, nil
	case '<':
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Kind: TokOp, Val: "<=", Pos: start}, nil
		}
		if l.pos < len(l.input) && l.input[l.pos] == '>' {
			l.pos++
			return Token{Kind: TokOp, Val: "<>", Pos: start}, nil
		}
		return Token{Kind: TokOp, Val: "<", Pos: start}, nil
	case '>':
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
		}
		return Token{Kind: TokOp, Val: l.input[start:l.pos], Pos: start}, nil
	case '!':
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '=' {
			l.pos++
			return Token{Kind: TokOp, Val: "!=", Pos: start}, nil
		}
		return Token{}, fmt.Errorf("lexer: unexpected '!' at position %d", start)
	}
	return Token{}, fmt.Errorf("lexer: unexpected character %q at position %d", c, start)
}

func (l *lexer) ident(start int) (Token, error) {
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if isIdentPart(c) {
			l.pos++
			continue
		}
		break
	}
	val := strings.ToLower(l.input[start:l.pos])
	return Token{Kind: TokIdent, Val: val, Pos: start}, nil
}

func (l *lexer) number(start int) (Token, error) {
	isFloat := false
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if c >= '0' && c <= '9' {
			l.pos++
			continue
		}
		if c == '.' && !isFloat {
			isFloat = true
			l.pos++
			continue
		}
		break
	}
	kind := TokInt
	if isFloat {
		kind = TokFloat
	}
	return Token{Kind: kind, Val: l.input[start:l.pos], Pos: start}, nil
}

func (l *lexer) string(start int) (Token, error) {
	l.pos++ // opening quote
	var sb strings.Builder
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if c == '\'' {
			if l.pos+1 < len(l.input) && l.input[l.pos+1] == '\'' {
				sb.WriteByte('\'')
				l.pos += 2
				continue
			}
			l.pos++ // closing quote
			return Token{Kind: TokString, Val: sb.String(), Pos: start}, nil
		}
		sb.WriteByte(c)
		l.pos++
	}
	return Token{}, fmt.Errorf("lexer: unterminated string starting at position %d", start)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
