package sql

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseError is a syntax error with a position.
type ParseError struct {
	Pos int
	Msg string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("sql: syntax error at position %d: %s", e.Pos, e.Msg)
}

type parser struct {
	toks []Token
	pos  int
}

// Parse parses a single SQL statement into a Select AST.
func Parse(input string) (*Select, error) {
	toks, err := Tokenize(input)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	sel, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	if p.peek().Kind != TokEOF {
		return nil, &ParseError{Pos: p.peek().Pos, Msg: fmt.Sprintf("unexpected token %q", p.peek().Val)}
	}
	return sel, nil
}

func (p *parser) peek() Token { return p.toks[p.pos] }

func (p *parser) next() Token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) expect(kind TokenKind, val string) (Token, error) {
	t := p.peek()
	if t.Kind != kind || (val != "" && t.Val != val) {
		return t, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("expected %s %q, got %q", kindName(kind), val, t.Val)}
	}
	p.next()
	return t, nil
}

func kindName(k TokenKind) string {
	switch k {
	case TokIdent:
		return "identifier"
	case TokInt:
		return "integer"
	case TokFloat:
		return "float"
	case TokString:
		return "string"
	case TokOp:
		return "operator"
	case TokComma:
		return "comma"
	case TokLParen:
		return "("
	case TokRParen:
		return ")"
	case TokStar:
		return "*"
	case TokDot:
		return "."
	}
	return "token"
}

func (p *parser) parseSelect() (*Select, error) {
	if _, err := p.expect(TokIdent, "select"); err != nil {
		return nil, err
	}
	sel := &Select{}
	if p.peek().Kind == TokIdent && p.peek().Val == "distinct" {
		p.next()
		sel.Distinct = true
	}
	exprs, err := p.parseSelectList()
	if err != nil {
		return nil, err
	}
	sel.Select = exprs

	if p.peek().Kind == TokIdent && p.peek().Val == "from" {
		p.next()
		from, joins, err := p.parseFrom()
		if err != nil {
			return nil, err
		}
		sel.From = from
		sel.Joins = joins
	}

	if p.peek().Kind == TokIdent && p.peek().Val == "where" {
		p.next()
		w, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		sel.Where = w
	}

	if p.peek().Kind == TokIdent && p.peek().Val == "group" {
		p.next()
		if _, err := p.expect(TokIdent, "by"); err != nil {
			return nil, err
		}
		gs, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		sel.GroupBy = gs
	}

	if p.peek().Kind == TokIdent && p.peek().Val == "having" {
		p.next()
		h, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		sel.Having = h
	}

	if p.peek().Kind == TokIdent && p.peek().Val == "order" {
		p.next()
		if _, err := p.expect(TokIdent, "by"); err != nil {
			return nil, err
		}
		items, err := p.parseOrderList()
		if err != nil {
			return nil, err
		}
		sel.OrderBy = items
	}

	if p.peek().Kind == TokIdent && p.peek().Val == "limit" {
		p.next()
		n, err := p.parseNumberLiteral()
		if err != nil {
			return nil, err
		}
		sel.Limit = &n
	}
	if p.peek().Kind == TokIdent && p.peek().Val == "offset" {
		p.next()
		n, err := p.parseNumberLiteral()
		if err != nil {
			return nil, err
		}
		sel.Offset = &n
	}
	return sel, nil
}

func (p *parser) parseNumberLiteral() (int64, error) {
	t := p.peek()
	if t.Kind != TokInt {
		return 0, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("expected integer, got %q", t.Val)}
	}
	p.next()
	n, err := strconv.ParseInt(t.Val, 10, 64)
	if err != nil {
		return 0, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("invalid integer %q", t.Val)}
	}
	return n, nil
}

func (p *parser) parseSelectList() ([]Expr, error) {
	var out []Expr
	for {
		t := p.peek()
		if t.Kind == TokStar {
			p.next()
			out = append(out, &Star{})
		} else if t.Kind == TokIdent && p.toks[p.pos+1].Kind == TokDot && p.toks[p.pos+2].Kind == TokStar {
			table := t.Val
			p.next() // ident
			p.next() // dot
			p.next() // star
			out = append(out, &Star{Table: table})
		} else {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			// optional alias: AS ident or bare ident
			alias := ""
			if p.peek().Kind == TokIdent && p.peek().Val == "as" {
				p.next()
				at := p.peek()
				if at.Kind != TokIdent {
					return nil, &ParseError{Pos: at.Pos, Msg: "expected alias name"}
				}
				p.next()
				alias = at.Val
			} else if p.peek().Kind == TokIdent && !isClauseStart(p.peek().Val) {
				// implicit alias, e.g. SELECT a + 1 b
				at := p.peek()
				p.next()
				alias = at.Val
			}
			if alias != "" {
				e = &Alias{Expr: e, Name: alias}
			}
			out = append(out, e)
		}
		if p.peek().Kind == TokComma {
			p.next()
			continue
		}
		break
	}
	return out, nil
}

func isClauseStart(s string) bool {
	switch s {
	case "from", "where", "group", "having", "order", "limit", "offset", "union", "on", "inner", "left", "cross", "join":
		return true
	}
	return false
}

func (p *parser) parseExprList() ([]Expr, error) {
	var out []Expr
	for {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if p.peek().Kind == TokComma {
			p.next()
			continue
		}
		break
	}
	return out, nil
}

func (p *parser) parseOrderList() ([]*OrderItem, error) {
	var out []*OrderItem
	for {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		desc := false
		if p.peek().Kind == TokIdent && p.peek().Val == "desc" {
			p.next()
			desc = true
		} else if p.peek().Kind == TokIdent && p.peek().Val == "asc" {
			p.next()
		}
		out = append(out, &OrderItem{Expr: e, Desc: desc})
		if p.peek().Kind == TokComma {
			p.next()
			continue
		}
		break
	}
	return out, nil
}

func (p *parser) parseFrom() ([]*TableRef, []*Join, error) {
	var from []*TableRef
	var joins []*Join
	// first table
	t1, err := p.parseTableRef()
	if err != nil {
		return nil, nil, err
	}
	from = append(from, t1)

	for {
		t := p.peek()
		switch {
		case t.Kind == TokComma:
			p.next()
			tn, err := p.parseTableRef()
			if err != nil {
				return nil, nil, err
			}
			from = append(from, tn)
		case t.Kind == TokIdent && (t.Val == "join" || t.Val == "inner" || t.Val == "left" || t.Val == "cross"):
			j, err := p.parseJoin()
			if err != nil {
				return nil, nil, err
			}
			joins = append(joins, j)
		default:
			return from, joins, nil
		}
	}
}

func (p *parser) parseTableRef() (*TableRef, error) {
	t := p.peek()
	if t.Kind != TokIdent {
		return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("expected table name, got %q", t.Val)}
	}
	p.next()
	ref := &TableRef{Name: t.Val, Alias: t.Val}
	// optional alias: AS ident or bare ident (but not a join keyword)
	nt := p.peek()
	if nt.Kind == TokIdent && nt.Val == "as" {
		p.next()
		at := p.peek()
		if at.Kind != TokIdent {
			return nil, &ParseError{Pos: at.Pos, Msg: "expected table alias"}
		}
		p.next()
		ref.Alias = at.Val
	} else if nt.Kind == TokIdent && !isJoinStart(nt.Val) && !isClauseStart(nt.Val) {
		p.next()
		ref.Alias = nt.Val
	}
	return ref, nil
}

func isJoinStart(s string) bool {
	switch s {
	case "join", "inner", "left", "cross", "right", "full", "on", "where", "group", "having", "order", "limit", "offset":
		return true
	}
	return false
}

func (p *parser) parseJoin() (*Join, error) {
	jt := JoinInner
	t := p.peek()
	switch t.Val {
	case "inner":
		p.next()
		if _, err := p.expect(TokIdent, "join"); err != nil {
			return nil, err
		}
		jt = JoinInner
	case "left":
		p.next()
		if p.peek().Kind == TokIdent && p.peek().Val == "outer" {
			p.next()
		}
		if _, err := p.expect(TokIdent, "join"); err != nil {
			return nil, err
		}
		jt = JoinLeft
	case "cross":
		p.next()
		if _, err := p.expect(TokIdent, "join"); err != nil {
			return nil, err
		}
		jt = JoinCross
	case "join":
		p.next()
		jt = JoinInner
	default:
		return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("expected join type, got %q", t.Val)}
	}
	tr, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	j := &Join{Type: jt, Table: tr}
	if jt != JoinCross {
		if _, err := p.expect(TokIdent, "on"); err != nil {
			return nil, err
		}
		on, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		j.On = on
	}
	return j, nil
}

// ---------- expressions ----------

func (p *parser) parseExpr() (Expr, error) { return p.parseOr() }

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().Kind == TokIdent && p.peek().Val == "or" {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinOp{Op: "OR", Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().Kind == TokIdent && p.peek().Val == "and" {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &BinOp{Op: "AND", Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (Expr, error) {
	if p.peek().Kind == TokIdent && p.peek().Val == "not" {
		p.next()
		e, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &UnaryOp{Op: "NOT", Expr: e}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (Expr, error) {
	left, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		not := false
		if t.Kind == TokIdent && t.Val == "not" {
			// NOT IN / NOT BETWEEN / NOT LIKE / IS NOT NULL handled below
			nt := p.toks[p.pos+1]
			if nt.Kind == TokIdent && (nt.Val == "in" || nt.Val == "between" || nt.Val == "like") {
				not = true
				p.next()
				t = p.peek()
			} else {
				return left, nil
			}
		}
		switch {
		case t.Kind == TokOp && isCmpOp(t.Val):
			p.next()
			right, err := p.parseAdd()
			if err != nil {
				return nil, err
			}
			left = &BinOp{Op: strings.ToUpper(t.Val), Left: left, Right: right}
		case t.Kind == TokIdent && t.Val == "is":
			p.next()
			neg := false
			if p.peek().Kind == TokIdent && p.peek().Val == "not" {
				p.next()
				neg = true
			}
			if _, err := p.expect(TokIdent, "null"); err != nil {
				return nil, err
			}
			left = &IsNull{Expr: left, Not: neg}
		case t.Kind == TokIdent && t.Val == "in":
			p.next()
			if _, err := p.expect(TokLParen, "("); err != nil {
				return nil, err
			}
			var list []Expr
			for {
				e, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				list = append(list, e)
				if p.peek().Kind == TokComma {
					p.next()
					continue
				}
				break
			}
			if _, err := p.expect(TokRParen, ")"); err != nil {
				return nil, err
			}
			left = &InList{Expr: left, List: list}
			if not {
				left = &UnaryOp{Op: "NOT", Expr: left}
			}
		case t.Kind == TokIdent && t.Val == "between":
			p.next()
			low, err := p.parseAdd()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TokIdent, "and"); err != nil {
				return nil, err
			}
			high, err := p.parseAdd()
			if err != nil {
				return nil, err
			}
			left = &Between{Expr: left, Low: low, High: high}
			if not {
				left = &UnaryOp{Op: "NOT", Expr: left}
			}
		case t.Kind == TokIdent && t.Val == "like":
			p.next()
			st := p.peek()
			if st.Kind != TokString {
				return nil, &ParseError{Pos: st.Pos, Msg: "LIKE pattern must be a string literal"}
			}
			p.next()
			left = &Like{Expr: left, Pattern: st.Val}
			if not {
				left = &UnaryOp{Op: "NOT", Expr: left}
			}
		default:
			return left, nil
		}
	}
}

func isCmpOp(s string) bool {
	switch s {
	case "=", "<>", "!=", "<", ">", "<=", ">=":
		return true
	}
	return false
}

func (p *parser) parseAdd() (Expr, error) {
	left, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for p.peek().Kind == TokOp && (p.peek().Val == "+" || p.peek().Val == "-") {
		op := p.next().Val
		right, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		left = &BinOp{Op: op, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseMul() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.Kind == TokOp && (t.Val == "*" || t.Val == "/") {
			op := p.next().Val
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &BinOp{Op: op, Left: left, Right: right}
			continue
		}
		// * is tokenized as TokStar; in expression context it means multiply
		if t.Kind == TokStar {
			p.next()
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &BinOp{Op: "*", Left: left, Right: right}
			continue
		}
		break
	}
	return left, nil
}

func (p *parser) parseUnary() (Expr, error) {
	if p.peek().Kind == TokOp && p.peek().Val == "-" {
		p.next()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryOp{Op: "-", Expr: e}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.Kind {
	case TokInt:
		p.next()
		n, err := strconv.ParseInt(t.Val, 10, 64)
		if err != nil {
			return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("invalid integer %q", t.Val)}
		}
		return &Literal{Kind: LitInt, Int: n}, nil
	case TokFloat:
		p.next()
		f, err := strconv.ParseFloat(t.Val, 64)
		if err != nil {
			return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("invalid float %q", t.Val)}
		}
		return &Literal{Kind: LitFloat, Float: f}, nil
	case TokString:
		p.next()
		return &Literal{Kind: LitString, Str: t.Val}, nil
	case TokIdent:
		switch t.Val {
		case "null":
			p.next()
			return &Literal{Kind: LitNull}, nil
		case "true":
			p.next()
			return &Literal{Kind: LitBool, Bool: true}, nil
		case "false":
			p.next()
			return &Literal{Kind: LitBool, Bool: false}, nil
		case "count", "sum", "avg", "min", "max":
			return p.parseAggregate(t.Val)
		default:
			if IsKeyword(t.Val) {
				return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("unexpected keyword %q", t.Val)}
			}
			p.next()
			ref := &ColumnRef{Name: t.Val}
			if p.peek().Kind == TokDot {
				p.next()
				nt := p.peek()
				if nt.Kind != TokIdent {
					return nil, &ParseError{Pos: nt.Pos, Msg: "expected column name after '.'"}
				}
				p.next()
				ref.Table = ref.Name
				ref.Name = nt.Val
			}
			return ref, nil
		}
	case TokLParen:
		p.next()
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRParen, ")"); err != nil {
			return nil, err
		}
		return e, nil
	}
	return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("expected expression, got %q", t.Val)}
}

func (p *parser) parseAggregate(name string) (Expr, error) {
	p.next() // function name
	if _, err := p.expect(TokLParen, "("); err != nil {
		return nil, err
	}
	agg := &Aggregate{Name: strings.ToUpper(name)}
	if name == "count" && p.peek().Kind == TokStar {
		p.next()
	} else {
		if p.peek().Kind == TokIdent && p.peek().Val == "distinct" {
			p.next()
			agg.Distinct = true
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		agg.Expr = e
	}
	if _, err := p.expect(TokRParen, ")"); err != nil {
		return nil, err
	}
	return agg, nil
}
