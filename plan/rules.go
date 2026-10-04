package plan

import (
	"sort"
	"strings"

	"github.com/guoyyit/query-optimizer-go/sql"
)

// RuleName identifies a rewrite rule.
type RuleName string

const (
	RulePredicatePushdown RuleName = "predicate_pushdown"
	RuleProjectionPruning RuleName = "projection_pruning"
	RuleJoinReorder       RuleName = "join_reorder"
	RuleConstantFolding   RuleName = "constant_folding"
)

// RewriteNote records what a rule did or why it could not apply.
type RewriteNote struct {
	Rule   RuleName
	Detail string
}

func note(rule RuleName, detail string) RewriteNote {
	return RewriteNote{Rule: rule, Detail: detail}
}

// ApplyRules runs the enabled rules over a logical plan and returns the
// rewritten plan together with rewrite notes.
func ApplyRules(root LogicalNode, enabled map[RuleName]bool) (LogicalNode, []RewriteNote) {
	var notes []RewriteNote
	if enabled[RuleConstantFolding] {
		root, notes = applyFolding(root, notes)
	}
	if enabled[RulePredicatePushdown] {
		var pn []RewriteNote
		root, pn = applyPredicatePushdown(root)
		notes = append(notes, pn...)
	}
	return root, notes
}

// ---------- helpers ----------

// aliasesOf returns the sorted set of table aliases referenced by an expr.
func aliasesOf(e sql.Expr) []string {
	set := map[string]bool{}
	for _, c := range sql.CollectColumns(e) {
		if c.Table != "" {
			set[c.Table] = true
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// collectAliases returns the set of aliases present in a subtree.
func collectAliases(n LogicalNode) map[string]bool {
	set := map[string]bool{}
	var walk func(x LogicalNode)
	walk = func(x LogicalNode) {
		switch v := x.(type) {
		case *Scan:
			set[v.Alias] = true
		case *Filter:
			walk(v.Child)
		case *Project:
			walk(v.Child)
		case *Join:
			walk(v.Left)
			walk(v.Right)
		case *Aggregate:
			walk(v.Child)
		case *Sort:
			walk(v.Child)
		case *Limit:
			walk(v.Child)
		case *Distinct:
			walk(v.Child)
		}
	}
	walk(n)
	return set
}

func hasAlias(n LogicalNode, alias string) bool {
	return collectAliases(n)[alias]
}

// ---------- predicate pushdown ----------

func applyPredicatePushdown(root LogicalNode) (LogicalNode, []RewriteNote) {
	return pushDown(root)
}

func pushDown(n LogicalNode) (LogicalNode, []RewriteNote) {
	var notes []RewriteNote
	switch x := n.(type) {
	case *Filter:
		child, cn := pushDown(x.Child)
		notes = append(notes, cn...)
		return pushFilterThrough(child, x.Pred, notes)
	case *Join:
		left, ln := pushDown(x.Left)
		notes = append(notes, ln...)
		right, rn := pushDown(x.Right)
		notes = append(notes, rn...)
		x.Left = left
		x.Right = right
		if x.Type == sql.JoinLeft && x.Cond != nil {
			conds := sql.SplitConjuncts(x.Cond)
			var kept []sql.Expr
			for _, c := range conds {
				refs := aliasesOf(c)
				if len(refs) == 1 {
					a := refs[0]
					if hasAlias(x.Left, a) && !hasAlias(x.Right, a) {
						x.Left = pushToSide(x.Left, c)
						notes = append(notes, note(RulePredicatePushdown, "pushed ON predicate to left side of LEFT JOIN: "+sql.ExprString(c)))
						continue
					}
					if hasAlias(x.Right, a) && !hasAlias(x.Left, a) {
						x.Right = pushToSide(x.Right, c)
						notes = append(notes, note(RulePredicatePushdown, "pushed ON predicate to right side of LEFT JOIN: "+sql.ExprString(c)))
						continue
					}
				}
				kept = append(kept, c)
			}
			x.Cond = sql.Conjoin(kept)
		}
		return x, notes
	case *Project:
		child, cn := pushDown(x.Child)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Aggregate:
		child, cn := pushDown(x.Child)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Sort:
		child, cn := pushDown(x.Child)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Limit:
		child, cn := pushDown(x.Child)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Distinct:
		child, cn := pushDown(x.Child)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	}
	return n, notes
}

// pushFilterThrough tries to push a filter predicate down through child.
func pushFilterThrough(child LogicalNode, pred sql.Expr, notes []RewriteNote) (LogicalNode, []RewriteNote) {
	conjuncts := sql.SplitConjuncts(pred)
	var remaining []sql.Expr
	for _, conj := range conjuncts {
		refs := aliasesOf(conj)
		if len(refs) == 0 {
			remaining = append(remaining, conj)
			continue
		}
		pushed := false
		if j, ok := child.(*Join); ok && j.Type != sql.JoinLeft {
			leftAliases := collectAliases(j.Left)
			rightAliases := collectAliases(j.Right)
			switch len(refs) {
			case 1:
				a := refs[0]
				if leftAliases[a] && !rightAliases[a] {
					j.Left = pushToSide(j.Left, conj)
					notes = append(notes, note(RulePredicatePushdown, "pushed predicate to left: "+sql.ExprString(conj)))
					pushed = true
				} else if rightAliases[a] && !leftAliases[a] {
					j.Right = pushToSide(j.Right, conj)
					notes = append(notes, note(RulePredicatePushdown, "pushed predicate to right: "+sql.ExprString(conj)))
					pushed = true
				}
			case 2:
				a, b := refs[0], refs[1]
				if (leftAliases[a] && rightAliases[b]) || (leftAliases[b] && rightAliases[a]) {
					if j.Type == sql.JoinCross {
						j.Type = sql.JoinInner
						notes = append(notes, note(RulePredicatePushdown, "converted CROSS JOIN to INNER JOIN on: "+sql.ExprString(conj)))
					}
					j.Cond = sql.Conjoin([]sql.Expr{j.Cond, conj})
					notes = append(notes, note(RulePredicatePushdown, "attached join condition: "+sql.ExprString(conj)))
					pushed = true
				}
			}
		}
		if !pushed {
			remaining = append(remaining, conj)
		}
	}
	if len(remaining) == 0 {
		return child, notes
	}
	return &Filter{Child: child, Pred: sql.Conjoin(remaining)}, notes
}

// pushToSide pushes a single-table predicate as far down as possible through
// inner joins; left joins and aggregates block pushing.
func pushToSide(node LogicalNode, pred sql.Expr) LogicalNode {
	switch x := node.(type) {
	case *Scan:
		return &Filter{Child: x, Pred: pred}
	case *Filter:
		x.Pred = sql.Conjoin([]sql.Expr{x.Pred, pred})
		return x
	case *Join:
		if x.Type == sql.JoinLeft {
			return &Filter{Child: node, Pred: pred}
		}
		refs := aliasesOf(pred)
		leftAliases := collectAliases(x.Left)
		rightAliases := collectAliases(x.Right)
		if len(refs) == 1 && leftAliases[refs[0]] {
			x.Left = pushToSide(x.Left, pred)
			return x
		}
		if len(refs) == 1 && rightAliases[refs[0]] {
			x.Right = pushToSide(x.Right, pred)
			return x
		}
		if len(refs) == 2 {
			if x.Type == sql.JoinCross {
				x.Type = sql.JoinInner
			}
			x.Cond = sql.Conjoin([]sql.Expr{x.Cond, pred})
			return x
		}
		return &Filter{Child: node, Pred: pred}
	default:
		return &Filter{Child: node, Pred: pred}
	}
}

// ---------- constant folding ----------

func applyFolding(root LogicalNode, notes []RewriteNote) (LogicalNode, []RewriteNote) {
	switch x := root.(type) {
	case *Filter:
		x.Pred = foldExpr(x.Pred)
		if lit, ok := x.Pred.(*sql.Literal); ok && lit.Kind == sql.LitBool {
			if lit.Bool {
				notes = append(notes, note(RuleConstantFolding, "filter folded to TRUE; removed"))
				return applyFolding(x.Child, notes)
			}
			notes = append(notes, note(RuleConstantFolding, "filter folded to FALSE; subtree yields no rows"))
			return x, notes
		}
		child, cn := applyFolding(x.Child, nil)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Join:
		if x.Cond != nil {
			x.Cond = foldExpr(x.Cond)
		}
		left, ln := applyFolding(x.Left, nil)
		notes = append(notes, ln...)
		right, rn := applyFolding(x.Right, nil)
		notes = append(notes, rn...)
		x.Left = left
		x.Right = right
		return x, notes
	case *Project:
		for i, e := range x.Exprs {
			x.Exprs[i] = foldExpr(e)
		}
		child, cn := applyFolding(x.Child, nil)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Aggregate:
		for i, e := range x.Group {
			x.Group[i] = foldExpr(e)
		}
		child, cn := applyFolding(x.Child, nil)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Sort:
		child, cn := applyFolding(x.Child, nil)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Limit:
		child, cn := applyFolding(x.Child, nil)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	case *Distinct:
		child, cn := applyFolding(x.Child, nil)
		notes = append(notes, cn...)
		x.Child = child
		return x, notes
	}
	return root, notes
}

// foldExpr evaluates constant subexpressions.
func foldExpr(e sql.Expr) sql.Expr {
	switch x := e.(type) {
	case *sql.BinOp:
		x.Left = foldExpr(x.Left)
		x.Right = foldExpr(x.Right)
		// boolean simplification
		if l, ok := x.Left.(*sql.Literal); ok && l.Kind == sql.LitBool {
			if strings.ToUpper(x.Op) == "AND" {
				if !l.Bool {
					return &sql.Literal{Kind: sql.LitBool, Bool: false}
				}
				return x.Right
			}
			if strings.ToUpper(x.Op) == "OR" {
				if l.Bool {
					return &sql.Literal{Kind: sql.LitBool, Bool: true}
				}
				return x.Right
			}
		}
		if r, ok := x.Right.(*sql.Literal); ok && r.Kind == sql.LitBool {
			if strings.ToUpper(x.Op) == "AND" {
				if !r.Bool {
					return &sql.Literal{Kind: sql.LitBool, Bool: false}
				}
				return x.Left
			}
			if strings.ToUpper(x.Op) == "OR" {
				if r.Bool {
					return &sql.Literal{Kind: sql.LitBool, Bool: true}
				}
				return x.Left
			}
		}
		// constant arithmetic/comparison
		if ll, lok := x.Left.(*sql.Literal); lok {
			if rl, rok := x.Right.(*sql.Literal); rok {
				if v, ok := evalConstBin(x.Op, ll, rl); ok {
					return v
				}
			}
		}
	case *sql.UnaryOp:
		x.Expr = foldExpr(x.Expr)
		if l, ok := x.Expr.(*sql.Literal); ok && l.Kind == sql.LitBool {
			if x.Op == "NOT" {
				return &sql.Literal{Kind: sql.LitBool, Bool: !l.Bool}
			}
		}
		if x.Op == "-" {
			if l, ok := x.Expr.(*sql.Literal); ok {
				switch l.Kind {
				case sql.LitInt:
					return &sql.Literal{Kind: sql.LitInt, Int: -l.Int}
				case sql.LitFloat:
					return &sql.Literal{Kind: sql.LitFloat, Float: -l.Float}
				}
			}
		}
	}
	return e
}

func evalConstBin(op string, l, r *sql.Literal) (sql.Expr, bool) {
	// only handle arithmetic on numeric literals and equality of literals
	switch op {
	case "+", "-", "*", "/":
		if l.Kind == sql.LitNull || r.Kind == sql.LitNull {
			return &sql.Literal{Kind: sql.LitNull}, true
		}
		lv, ok1 := literalFloat(l)
		rv, ok2 := literalFloat(r)
		if !ok1 || !ok2 {
			return nil, false
		}
		var v float64
		switch op {
		case "+":
			v = lv + rv
		case "-":
			v = lv - rv
		case "*":
			v = lv * rv
		case "/":
			if rv == 0 {
				return &sql.Literal{Kind: sql.LitNull}, true
			}
			v = lv / rv
		}
		if l.Kind == sql.LitInt && r.Kind == sql.LitInt && op != "/" {
			return &sql.Literal{Kind: sql.LitInt, Int: int64(v)}, true
		}
		if l.Kind == sql.LitInt && r.Kind == sql.LitInt && op == "/" && lv == float64(int64(lv)) && rv == float64(int64(rv)) && int64(lv)%int64(rv) == 0 {
			return &sql.Literal{Kind: sql.LitInt, Int: int64(v)}, true
		}
		return &sql.Literal{Kind: sql.LitFloat, Float: v}, true
	case "=", "<>", "!=", "<", ">", "<=", ">=":
		if l.Kind == sql.LitNull || r.Kind == sql.LitNull {
			return &sql.Literal{Kind: sql.LitNull}, true
		}
		cmp := compareLiterals(l, r)
		var b bool
		switch op {
		case "=":
			b = cmp == 0
		case "<>", "!=":
			b = cmp != 0
		case "<":
			b = cmp < 0
		case ">":
			b = cmp > 0
		case "<=":
			b = cmp <= 0
		case ">=":
			b = cmp >= 0
		}
		return &sql.Literal{Kind: sql.LitBool, Bool: b}, true
	}
	return nil, false
}

func literalFloat(l *sql.Literal) (float64, bool) {
	switch l.Kind {
	case sql.LitInt:
		return float64(l.Int), true
	case sql.LitFloat:
		return l.Float, true
	}
	return 0, false
}

func compareLiterals(l, r *sql.Literal) int {
	if l.Kind == sql.LitInt || l.Kind == sql.LitFloat {
		if r.Kind == sql.LitInt || r.Kind == sql.LitFloat {
			lv, _ := literalFloat(l)
			rv, _ := literalFloat(r)
			if lv < rv {
				return -1
			}
			if lv > rv {
				return 1
			}
			return 0
		}
	}
	if l.Kind != r.Kind {
		if l.Kind < r.Kind {
			return -1
		}
		return 1
	}
	if l.Kind == sql.LitString {
		return strings.Compare(l.Str, r.Str)
	}
	if l.Kind == sql.LitBool {
		if l.Bool == r.Bool {
			return 0
		}
		if !l.Bool {
			return -1
		}
		return 1
	}
	return 0
}
