package sql

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/value"
)

// ParseError pinpoints a syntax problem.
type ParseError struct {
	Pos int
	Msg string
}

func (e *ParseError) Error() string { return fmt.Sprintf("parse error at %d: %s", e.Pos, e.Msg) }

type token struct {
	kind string // ident, number, string, op, eof
	text string
	pos  int
}

type lexer struct {
	src  string
	pos  int
	toks []token
}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case c == '(' || c == ')' || c == ',' || c == '.' || c == '*':
			toks = append(toks, token{"sym", string(c), i})
			i++
		case c == '=' || c == '<' || c == '>' || c == '!':
			j := i + 1
			if j < len(src) && (src[j] == '=') {
				j++
			}
			toks = append(toks, token{"op", src[i:j], i})
			i = j
		case c == '\'':
			j := i + 1
			var sb strings.Builder
			for j < len(src) && src[j] != '\'' {
				sb.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, &ParseError{i, "unterminated string literal"}
			}
			toks = append(toks, token{"string", sb.String(), i})
			i = j + 1
		case unicode.IsDigit(rune(c)) || (c == '-' && i+1 < len(src) && unicode.IsDigit(rune(src[i+1]))):
			j := i + 1 // skip optional leading '-'
			for j < len(src) && (unicode.IsDigit(rune(src[j])) || src[j] == '.') {
				j++
			}
			toks = append(toks, token{"number", src[i:j], i})
			i = j
		case unicode.IsLetter(rune(c)) || c == '_':
			j := i
			for j < len(src) && (unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j])) || src[j] == '_') {
				j++
			}
			toks = append(toks, token{"ident", src[i:j], i})
			i = j
		default:
			return nil, &ParseError{i, fmt.Sprintf("unexpected character %q", c)}
		}
	}
	toks = append(toks, token{"eof", "", len(src)})
	return toks, nil
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }
func (p *parser) err(msg string) error {
	return &ParseError{p.peek().pos, msg}
}

func (p *parser) expectKw(kw string) error {
	t := p.peek()
	if t.kind == "ident" && strings.EqualFold(t.text, kw) {
		p.next()
		return nil
	}
	return p.err("expected " + kw)
}

func (p *parser) acceptKw(kw string) bool {
	t := p.peek()
	if t.kind == "ident" && strings.EqualFold(t.text, kw) {
		p.next()
		return true
	}
	return false
}

func (p *parser) acceptSym(s string) bool {
	t := p.peek()
	if t.kind == "sym" && t.text == s {
		p.next()
		return true
	}
	return false
}

// Parse parses the SQL subset into a SelectStmt.
func Parse(query string) (*SelectStmt, error) {
	toks, err := lex(query)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	stmt := &SelectStmt{}
	if err := p.expectKw("SELECT"); err != nil {
		return nil, err
	}
	if p.acceptSym("*") {
		stmt.Star = true
	} else {
		for {
			ref, err := p.parseColumnRef()
			if err != nil {
				return nil, err
			}
			stmt.Projections = append(stmt.Projections, *ref)
			if !p.acceptSym(",") {
				break
			}
		}
	}
	if err := p.expectKw("FROM"); err != nil {
		return nil, err
	}
	first, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	stmt.Tables = append(stmt.Tables, *first)
	for {
		if p.acceptKw("JOIN") {
			tref, err := p.parseTableRef()
			if err != nil {
				return nil, err
			}
			stmt.Tables = append(stmt.Tables, *tref)
			if err := p.expectKw("ON"); err != nil {
				return nil, err
			}
			on, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			stmt.Joins = append(stmt.Joins, JoinClause{On: on})
			continue
		}
		if p.acceptSym(",") {
			// comma join: no condition; WHERE predicates may later be
			// associated with it by the optimizer
			tref, err := p.parseTableRef()
			if err != nil {
				return nil, err
			}
			stmt.Tables = append(stmt.Tables, *tref)
			stmt.Joins = append(stmt.Joins, JoinClause{On: nil})
			continue
		}
		break
	}
	if p.acceptKw("WHERE") {
		w, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		stmt.Where = w
	}
	if p.peek().kind != "eof" {
		return nil, p.err("unexpected trailing input")
	}
	return stmt, nil
}

func (p *parser) parseTableRef() (*TableRef, error) {
	t := p.next()
	if t.kind != "ident" {
		return nil, p.err("expected table name")
	}
	ref := &TableRef{Table: t.text, Alias: t.text}
	// optional [AS] alias
	save := p.i
	p.acceptKw("AS")
	if nt := p.peek(); nt.kind == "ident" && !isKeyword(nt.text) {
		ref.Alias = p.next().text
	} else {
		p.i = save
	}
	return ref, nil
}

func isKeyword(s string) bool {
	switch strings.ToUpper(s) {
	case "SELECT", "FROM", "WHERE", "JOIN", "ON", "AND", "OR", "NOT", "AS":
		return true
	}
	return false
}

func (p *parser) parseColumnRef() (*expr.ColumnRef, error) {
	t := p.next()
	if t.kind != "ident" {
		return nil, p.err("expected column name")
	}
	if p.acceptSym(".") {
		t2 := p.next()
		if t2.kind != "ident" {
			return nil, p.err("expected column name after '.'")
		}
		return &expr.ColumnRef{Table: t.text, Column: t2.text}, nil
	}
	return &expr.ColumnRef{Column: t.text}, nil
}

func (p *parser) parseOr() (expr.Expr, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.acceptKw("OR") {
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &expr.Binary{Op: expr.Or, L: l, R: r}
	}
	return l, nil
}

func (p *parser) parseAnd() (expr.Expr, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.acceptKw("AND") {
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = &expr.Binary{Op: expr.And, L: l, R: r}
	}
	return l, nil
}

func (p *parser) parseNot() (expr.Expr, error) {
	if p.acceptKw("NOT") {
		e, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &expr.Not{E: e}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (expr.Expr, error) {
	if p.acceptSym("(") {
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.acceptSym(")") {
			return nil, p.err("expected ')'")
		}
		return e, nil
	}
	l, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == "op" {
		p.next()
		r, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		var op expr.Op
		switch t.text {
		case "=":
			op = expr.Eq
		case "!=":
			op = expr.Ne
		case "<":
			op = expr.Lt
		case "<=":
			op = expr.Le
		case ">":
			op = expr.Gt
		case ">=":
			op = expr.Ge
		default:
			return nil, &ParseError{t.pos, "unsupported operator " + t.text}
		}
		return &expr.Binary{Op: op, L: l, R: r}, nil
	}
	return l, nil
}

func (p *parser) parseOperand() (expr.Expr, error) {
	t := p.next()
	switch t.kind {
	case "number":
		if strings.Contains(t.text, ".") {
			f, err := strconv.ParseFloat(t.text, 64)
			if err != nil {
				return nil, &ParseError{t.pos, "bad number " + t.text}
			}
			return &expr.Literal{V: value.FromFloat(f)}, nil
		}
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, &ParseError{t.pos, "bad number " + t.text}
		}
		return &expr.Literal{V: value.FromInt(n)}, nil
	case "string":
		return &expr.Literal{V: value.FromString(t.text)}, nil
	case "ident":
		switch strings.ToUpper(t.text) {
		case "TRUE":
			return &expr.Literal{V: value.FromBool(true)}, nil
		case "FALSE":
			return &expr.Literal{V: value.FromBool(false)}, nil
		case "NULL":
			return &expr.Literal{V: value.NullValue()}, nil
		}
		if p.acceptSym(".") {
			t2 := p.next()
			if t2.kind != "ident" {
				return nil, p.err("expected column name after '.'")
			}
			return &expr.ColumnRef{Table: t.text, Column: t2.text}, nil
		}
		return &expr.ColumnRef{Column: t.text}, nil
	}
	return nil, &ParseError{t.pos, "expected operand"}
}
