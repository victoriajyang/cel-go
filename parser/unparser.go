// Copyright 2019 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/cel-go/common/operators"

	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// Unparse takes an input expression and source position information and generates a human-readable
// expression.
//
// Note, unparsing an AST will often generate the same expression as was originally parsed, but some
// formatting may be lost in translation, notably:
//
// - All quoted literals are doubled quoted.
// - Byte literals are represented as utf8-string rather than byte, octal, or unicode escapes.
// - Floating point values are converted to the small number of digits needed to represent the value.
// - Spacing around punctuation marks may be lost.
// - Parentheses will only be applied when they affect operator precedence.
func Unparse(expr *exprpb.Expr, info *exprpb.SourceInfo) (string, error) {
	un := &unparser{info: info}
	err := un.visit(expr)
	if err != nil {
		return "", err
	}
	// Test whether newlines need to be applied.
	breaks := info.GetLineOffsets()
	if len(breaks) <= 1 {
		return un.str.String(), nil
	}
	// Apply the newlines.
	txt := []rune(un.str.String())
	sz := int32(len(txt))
	for _, br := range breaks {
		for ; br-1 < sz; br++ {
			if txt[br-1] == rune(' ') {
				txt[br-1] = rune('\n')
				break
			}
		}
	}
	return string(txt), nil
}

// unparser visits an expression to reconstruct a human-readable string from an AST.
type unparser struct {
	info   *exprpb.SourceInfo
	str    strings.Builder
	offset int32
}

func (un *unparser) visit(expr *exprpb.Expr) error {
	switch expr.ExprKind.(type) {
	case *exprpb.Expr_CallExpr:
		return un.visitCall(expr)
	// TODO: Comprehensions are currently not supported.
	case *exprpb.Expr_ComprehensionExpr:
		return un.visitComprehension(expr)
	case *exprpb.Expr_ConstExpr:
		return un.visitConst(expr)
	case *exprpb.Expr_IdentExpr:
		return un.visitIdent(expr)
	case *exprpb.Expr_ListExpr:
		return un.visitList(expr)
	case *exprpb.Expr_SelectExpr:
		return un.visitSelect(expr)
	case *exprpb.Expr_StructExpr:
		return un.visitStruct(expr)
	}
	return fmt.Errorf("unsupported expr: %v", expr)
}

func (un *unparser) visitCall(expr *exprpb.Expr) error {
	c := expr.GetCallExpr()
	fun := c.GetFunction()
	switch fun {
	// ternary operator
	case operators.Conditional:
		return un.visitCallConditional(expr)
	// index operator
	case operators.Index:
		return un.visitCallIndex(expr)
	// unary operators
	case operators.LogicalNot, operators.Negate:
		return un.visitCallUnary(expr)
	// binary operators
	case operators.Add,
		operators.Divide,
		operators.Equals,
		operators.Greater,
		operators.GreaterEquals,
		operators.In,
		operators.Less,
		operators.LessEquals,
		operators.LogicalAnd,
		operators.LogicalOr,
		operators.Modulo,
		operators.Multiply,
		operators.NotEquals,
		operators.OldIn,
		operators.Subtract:
		return un.visitCallBinary(expr)
	// standard function calls.
	default:
		return un.visitCallFunc(expr)
	}
}

func (un *unparser) visitCallBinary(expr *exprpb.Expr) error {
	c := expr.GetCallExpr()
	fun := c.GetFunction()
	args := c.GetArgs()
	lhs := args[0]
	// add parens if the current operator is lower precedence than the lhs expr operator.
	lhsParen := isLowerPrecedence(fun, lhs)
	rhs := args[1]
	// add parens if the current operator is lower precedence than the rhs expr operator,
	// or the same precedence and the operator is left recursive.
	rhsParen := isLowerPrecedence(fun, rhs)
	if !rhsParen && isLeftRecursive(fun) {
		rhsParen = isSamePrecedence(fun, rhs)
	}
	err := un.visitMaybeNested(lhs, lhsParen)
	if err != nil {
		return err
	}
	unmangled, found := operators.FindReverse(fun)
	if !found {
		return fmt.Errorf("cannot unmangle operator: %s", fun)
	}
	un.str.WriteString(" ")
	un.pad(expr.GetId())
	un.str.WriteString(unmangled)
	un.str.WriteString(" ")
	return un.visitMaybeNested(rhs, rhsParen)
}

func (un *unparser) visitCallConditional(expr *exprpb.Expr) error {
	c := expr.GetCallExpr()
	args := c.GetArgs()
	// add parens if operand is a conditional itself.
	nested := isSamePrecedence(operators.Conditional, args[0])
	err := un.visitMaybeNested(args[0], nested)
	if err != nil {
		return err
	}
	un.pad(expr.GetId())
	un.str.WriteString("? ")
	// add parens if operand is a conditional itself.
	nested = isSamePrecedence(operators.Conditional, args[1])
	err = un.visitMaybeNested(args[1], nested)
	if err != nil {
		return err
	}
	un.str.WriteString(" : ")
	// add parens if operand is a conditional itself.
	nested = isSamePrecedence(operators.Conditional, args[2])
	return un.visitMaybeNested(args[2], nested)
}

func (un *unparser) visitCallFunc(expr *exprpb.Expr) error {
	c := expr.GetCallExpr()
	fun := c.GetFunction()
	args := c.GetArgs()
	if c.GetTarget() != nil {
		err := un.visit(c.GetTarget())
		if err != nil {
			return err
		}
		un.str.WriteString(".")
	}
	un.str.WriteString(fun)
	un.pad(expr.GetId())
	un.str.WriteString("(")
	for i, arg := range args {
		err := un.visit(arg)
		if err != nil {
			return err
		}
		if i < len(args)-1 {
			un.str.WriteString(",")
		}
	}
	un.str.WriteString(")")
	return nil
}

func (un *unparser) visitCallIndex(expr *exprpb.Expr) error {
	c := expr.GetCallExpr()
	args := c.GetArgs()
	err := un.visit(args[0])
	if err != nil {
		return err
	}
	un.pad(expr.GetId())
	un.str.WriteString("[")
	err = un.visit(args[1])
	if err != nil {
		return err
	}
	un.str.WriteString("]")
	return nil
}

func (un *unparser) visitCallUnary(expr *exprpb.Expr) error {
	un.pad(expr.GetId())
	c := expr.GetCallExpr()
	fun := c.GetFunction()
	args := c.GetArgs()
	unmangled, found := operators.FindReverse(fun)
	if !found {
		return fmt.Errorf("cannot unmangle operator: %s", fun)
	}
	un.str.WriteString(unmangled)
	return un.visit(args[0])
}

func (un *unparser) visitComprehension(expr *exprpb.Expr) error {
	// TODO: introduce a macro expansion map between the top-level comprehension id and the
	// function call that the macro replaces.
	return fmt.Errorf("unimplemented : %v", expr)
}

func (un *unparser) visitConst(expr *exprpb.Expr) error {
	un.pad(expr.GetId())
	c := expr.GetConstExpr()
	switch c.ConstantKind.(type) {
	case *exprpb.Constant_BoolValue:
		un.str.WriteString(strconv.FormatBool(c.GetBoolValue()))
	case *exprpb.Constant_BytesValue:
		// bytes constants are surrounded with b"<bytes>"
		b := c.GetBytesValue()
		un.str.WriteString(`b"`)
		un.str.Write(b)
		un.str.WriteString(`"`)
	case *exprpb.Constant_DoubleValue:
		// represent the float using the minimum required digits
		d := strconv.FormatFloat(c.GetDoubleValue(), 'g', -1, 64)
		un.str.WriteString(d)
	case *exprpb.Constant_Int64Value:
		i := strconv.FormatInt(c.GetInt64Value(), 10)
		un.str.WriteString(i)
	case *exprpb.Constant_NullValue:
		un.str.WriteString("null")
	case *exprpb.Constant_StringValue:
		// strings will be double quoted with quotes escaped.
		un.str.WriteString(strconv.Quote(c.GetStringValue()))
	case *exprpb.Constant_Uint64Value:
		// uint literals have a 'u' suffix.
		ui := strconv.FormatUint(c.GetUint64Value(), 10)
		un.str.WriteString(ui)
		un.str.WriteString("u")
	default:
		return fmt.Errorf("unimplemented : %v", expr)
	}
	return nil
}

func (un *unparser) visitIdent(expr *exprpb.Expr) error {
	un.pad(expr.GetId())
	un.str.WriteString(expr.GetIdentExpr().GetName())
	return nil
}

func (un *unparser) visitList(expr *exprpb.Expr) error {
	l := expr.GetListExpr()
	elems := l.GetElements()
	un.pad(expr.GetId())
	un.str.WriteString("[")
	for i, elem := range elems {
		err := un.visit(elem)
		if err != nil {
			return err
		}
		if i < len(elems)-1 {
			un.str.WriteString(",")
		}
	}
	un.str.WriteString("]")
	return nil
}

func (un *unparser) visitSelect(expr *exprpb.Expr) error {
	sel := expr.GetSelectExpr()
	// handle the case when the select expression was generated by the has() macro.
	if sel.GetTestOnly() {
		un.str.WriteString("has(")
	}
	err := un.visit(sel.GetOperand())
	if err != nil {
		return err
	}
	un.pad(expr.GetId())
	un.str.WriteString(".")
	un.str.WriteString(sel.GetField())
	if sel.GetTestOnly() {
		un.str.WriteString(")")
	}
	return nil
}

func (un *unparser) visitStruct(expr *exprpb.Expr) error {
	s := expr.GetStructExpr()
	// If the message name is non-empty, then this should be treated as message construction.
	if s.GetMessageName() != "" {
		return un.visitStructMsg(expr)
	}
	// Otherwise, build a map.
	return un.visitStructMap(expr)
}

func (un *unparser) visitStructMsg(expr *exprpb.Expr) error {
	m := expr.GetStructExpr()
	entries := m.GetEntries()
	un.str.WriteString(m.GetMessageName())
	un.pad(expr.GetId())
	un.str.WriteString("{")
	for i, entry := range entries {
		f := entry.GetFieldKey()
		un.str.WriteString(f)
		un.pad(entry.GetId())
		un.str.WriteString(": ")
		v := entry.GetValue()
		err := un.visit(v)
		if err != nil {
			return err
		}
		if i < len(entries)-1 {
			un.str.WriteString(", ")
		}
	}
	un.str.WriteString("}")
	return nil
}

func (un *unparser) visitStructMap(expr *exprpb.Expr) error {
	m := expr.GetStructExpr()
	entries := m.GetEntries()
	un.pad(expr.GetId())
	un.str.WriteString("{")
	for i, entry := range entries {
		k := entry.GetMapKey()
		err := un.visit(k)
		if err != nil {
			return err
		}
		un.pad(entry.GetId())
		un.str.WriteString(": ")
		v := entry.GetValue()
		err = un.visit(v)
		if err != nil {
			return err
		}
		if i < len(entries)-1 {
			un.str.WriteString(", ")
		}
	}
	un.str.WriteString("}")
	return nil
}

func (un *unparser) visitMaybeNested(expr *exprpb.Expr, nested bool) error {
	if nested {
		un.str.WriteString("(")
	}
	err := un.visit(expr)
	if err != nil {
		return err
	}
	if nested {
		un.str.WriteString(")")
	}
	return nil
}

// pos returns the source character offset of the expression id.
func (un *unparser) pos(id int64) int32 {
	return un.info.GetPositions()[id]
}

// pad potentially adds spaces from the current string builder position to the original position
// of the input expression id.
func (un *unparser) pad(id int64) {
	last := int32(un.str.Len())
	next := un.pos(id)
	for ; last < next; last++ {
		un.str.WriteString(" ")
	}
}

// isLeftRecursive indicates whether the parser resolves the call in a left-recursive manner as
// this can have an effect of how parentheses affect the order of operations in the AST.
func isLeftRecursive(op string) bool {
	return op != operators.LogicalAnd && op != operators.LogicalOr
}

// isSamePrecedence indicates whether the precedence of the input operator is the same as the
// precedence of the (possible) operation represented in the input Expr.
//
// If the expr is not a Call, the result is false.
func isSamePrecedence(op string, expr *exprpb.Expr) bool {
	if expr.GetCallExpr() == nil {
		return false
	}
	c := expr.GetCallExpr()
	other := c.GetFunction()
	return operators.Precedence(op) == operators.Precedence(other)
}

// isLowerPrecedence indicates whether the precedence of the input operator is lower precedence
// than the (possible) operation represented in the input Expr.
//
// If the expr is not a Call, the result is false.
func isLowerPrecedence(op string, expr *exprpb.Expr) bool {
	if expr.GetCallExpr() == nil {
		return false
	}
	c := expr.GetCallExpr()
	other := c.GetFunction()
	return operators.Precedence(op) < operators.Precedence(other)
}
